package identity

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	HeaderUserLogin      = "Tailscale-User-Login"
	HeaderUserName       = "Tailscale-User-Name"
	HeaderUserProfilePic = "Tailscale-User-Profile-Pic"
)

var (
	ErrIdentityUnavailable = errors.New("identity is unavailable")
	ErrUntrustedSource     = errors.New("identity source is not trusted")
	ErrInvalidIdentity     = errors.New("identity is invalid")
)

type Principal struct {
	UserID            string
	LoginName         string
	TailnetNode       string
	Organization      string
	DisplayName       string
	ProfilePictureURL string
	Source            string
}

type Extractor interface {
	Extract(request *http.Request) (Principal, error)
}

// SourceTrust authenticates the immediate TCP peer. Implementations must not
// inspect X-Forwarded-For: those values are controlled by an upstream client
// unless a separate, fully trusted proxy chain has already validated them.
type SourceTrust interface {
	Trusted(ip net.IP) bool
}

type CIDRTrust struct {
	networks []*net.IPNet
}

func NewCIDRTrust(cidrs ...string) (*CIDRTrust, error) {
	if len(cidrs) == 0 {
		return nil, errors.New("at least one trusted proxy CIDR is required")
	}
	trust := &CIDRTrust{networks: make([]*net.IPNet, 0, len(cidrs))}
	for _, value := range cidrs {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("parse trusted proxy CIDR %q: %w", value, err)
		}
		trust.networks = append(trust.networks, network)
	}
	return trust, nil
}

func (t *CIDRTrust) Trusted(ip net.IP) bool {
	if t == nil || ip == nil {
		return false
	}
	for index := range t.networks {
		if t.networks[index].Contains(ip) {
			return true
		}
	}
	return false
}

type LoopbackTrust struct{}

func (LoopbackTrust) Trusted(ip net.IP) bool { return ip != nil && ip.IsLoopback() }

// ServeHeaderExtractor is safe only when the immediate trusted peer is
// Tailscale Serve (or a proxy with equivalent behavior) and that peer removes
// all client-supplied Tailscale-User-* headers before injecting its own.
// Merely receiving these headers is never proof of identity.
type ServeHeaderExtractor struct {
	trust SourceTrust
}

func NewServeHeaderExtractor(trust SourceTrust) (*ServeHeaderExtractor, error) {
	if trust == nil {
		return nil, errors.New("trusted Serve source policy is required")
	}
	return &ServeHeaderExtractor{trust: trust}, nil
}

func (e *ServeHeaderExtractor) Extract(request *http.Request) (Principal, error) {
	if request == nil {
		return Principal{}, fmt.Errorf("%w: request is nil", ErrInvalidIdentity)
	}
	peer, err := immediatePeerIP(request.RemoteAddr)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUntrustedSource, err)
	}
	if !e.trust.Trusted(peer) {
		return Principal{}, fmt.Errorf("%w: immediate peer %s is outside the trusted Serve boundary", ErrUntrustedSource, peer)
	}
	login, err := singleHeader(request.Header, HeaderUserLogin, true, 320)
	if err != nil {
		return Principal{}, err
	}
	name, err := singleHeader(request.Header, HeaderUserName, false, 512)
	if err != nil {
		return Principal{}, err
	}
	picture, err := singleHeader(request.Header, HeaderUserProfilePic, false, 2048)
	if err != nil {
		return Principal{}, err
	}
	if picture != "" {
		parsed, parseErr := url.Parse(picture)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return Principal{}, fmt.Errorf("%w: %s must be an absolute HTTPS URL", ErrInvalidIdentity, HeaderUserProfilePic)
		}
	}
	return Principal{
		LoginName:         login,
		DisplayName:       name,
		ProfilePictureURL: picture,
		Source:            "tailscale-serve",
	}, nil
}

// DevelopmentExtractor returns the configured identity by default. On a
// trusted development source it may accept X-AgentBox-Dev-User to exercise
// multi-user flows locally. It must never be enabled on a non-loopback peer.
type DevelopmentExtractor struct {
	enabled   bool
	principal Principal
	trust     SourceTrust
}

func NewDevelopmentExtractor(enabled bool, principal Principal, trust SourceTrust) (*DevelopmentExtractor, error) {
	if trust == nil {
		return nil, errors.New("development source policy is required")
	}
	if enabled {
		if err := validatePrincipal(principal); err != nil {
			return nil, err
		}
	}
	principal.Source = "development"
	return &DevelopmentExtractor{enabled: enabled, principal: principal, trust: trust}, nil
}

func (e *DevelopmentExtractor) Extract(request *http.Request) (Principal, error) {
	if !e.enabled {
		return Principal{}, ErrIdentityUnavailable
	}
	if request == nil {
		return Principal{}, fmt.Errorf("%w: request is nil", ErrInvalidIdentity)
	}
	peer, err := immediatePeerIP(request.RemoteAddr)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrUntrustedSource, err)
	}
	if !e.trust.Trusted(peer) {
		return Principal{}, fmt.Errorf("%w: development identity refused for peer %s", ErrUntrustedSource, peer)
	}
	principal := e.principal
	if login, headerErr := singleHeader(request.Header, "X-AgentBox-Dev-User", false, 320); headerErr != nil {
		return Principal{}, headerErr
	} else if login != "" {
		principal.LoginName = login
		principal.DisplayName = login
		if before, _, ok := strings.Cut(login, "@"); ok && before != "" {
			principal.DisplayName = before
		}
		principal.UserID = ""
	}
	principal.Source = "development"
	return principal, nil
}

type FallbackExtractor struct {
	primary     Extractor
	development Extractor
}

func NewFallbackExtractor(primary, development Extractor) (*FallbackExtractor, error) {
	if primary == nil || development == nil {
		return nil, errors.New("primary and development identity extractors are required")
	}
	return &FallbackExtractor{primary: primary, development: development}, nil
}

func (e *FallbackExtractor) Extract(request *http.Request) (Principal, error) {
	principal, err := e.primary.Extract(request)
	if err == nil {
		return principal, nil
	}
	// Never turn an untrusted or malformed production identity into a dev
	// identity. Development fallback is only for an otherwise trusted request
	// where Tailscale Serve supplied no user identity.
	if !errors.Is(err, ErrIdentityUnavailable) {
		return Principal{}, err
	}
	return e.development.Extract(request)
}

func immediatePeerIP(remoteAddress string) (net.IP, error) {
	if remoteAddress == "" {
		return nil, errors.New("request has no remote address")
	}
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return nil, fmt.Errorf("remote address %q is not an IP address", remoteAddress)
	}
	return ip, nil
}

func singleHeader(headers http.Header, name string, required bool, maximumBytes int) (string, error) {
	values := headers.Values(name)
	if len(values) == 0 {
		if required {
			return "", ErrIdentityUnavailable
		}
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("%w: %s must occur exactly once", ErrInvalidIdentity, name)
	}
	value := strings.TrimSpace(values[0])
	if required && value == "" {
		return "", ErrIdentityUnavailable
	}
	if len(value) > maximumBytes || !utf8.ValidString(value) || containsControl(value) {
		return "", fmt.Errorf("%w: %s contains invalid data", ErrInvalidIdentity, name)
	}
	return value, nil
}

func validatePrincipal(principal Principal) error {
	login := strings.TrimSpace(principal.LoginName)
	if login == "" || len(login) > 320 || !utf8.ValidString(login) || containsControl(login) {
		return fmt.Errorf("%w: development login is invalid", ErrInvalidIdentity)
	}
	if len(principal.DisplayName) > 512 || !utf8.ValidString(principal.DisplayName) || containsControl(principal.DisplayName) {
		return fmt.Errorf("%w: development display name is invalid", ErrInvalidIdentity)
	}
	return nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

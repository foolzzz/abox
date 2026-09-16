package domain

type HostStatus string

const (
	HostEnrolling HostStatus = "enrolling"
	HostOnline    HostStatus = "online"
	HostDraining  HostStatus = "draining"
	HostOffline   HostStatus = "offline"
	HostRevoked   HostStatus = "revoked"
)

type BoxStatus string

const (
	BoxCreated         BoxStatus = "created"
	BoxStarting        BoxStatus = "starting"
	BoxIdle            BoxStatus = "idle"
	BoxRunning         BoxStatus = "running"
	BoxWaitingApproval BoxStatus = "waiting_approval"
	BoxHibernating     BoxStatus = "hibernating"
	BoxHibernated      BoxStatus = "hibernated"
	BoxError           BoxStatus = "error"
	BoxTerminated      BoxStatus = "terminated"
)

type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunDispatching     RunStatus = "dispatching"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunInterrupting    RunStatus = "interrupting"
	RunDisconnected    RunStatus = "disconnected"
	RunSucceeded       RunStatus = "succeeded"
	RunFailed          RunStatus = "failed"
	RunAborted         RunStatus = "aborted"
	RunLost            RunStatus = "lost"
	RunCancelled       RunStatus = "cancelled"
)

type Delivery string

const (
	DeliveryPrompt   Delivery = "prompt"
	DeliverySteer    Delivery = "steer"
	DeliveryFollowUp Delivery = "follow_up"
)

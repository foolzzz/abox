package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronExpression is a parsed five-field cron expression (minute, hour, day of
// month, month, day of week). Day-of-month and day-of-week use the traditional
// cron OR semantics when both fields are restricted.
type CronExpression struct {
	minute      cronField
	hour        cronField
	dayOfMonth  cronField
	month       cronField
	dayOfWeek   cronField
	domWildcard bool
	dowWildcard bool
}

type cronField struct {
	min     int
	max     int
	allowed []bool
}

var cronAliases = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var cronMonths = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var cronWeekdays = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func ParseCronExpression(value string) (CronExpression, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if alias, ok := cronAliases[value]; ok {
		value = alias
	}
	parts := strings.Fields(value)
	if len(parts) != 5 {
		return CronExpression{}, fmt.Errorf("cron expression must contain exactly five fields")
	}
	minute, err := parseCronField(parts[0], 0, 59, nil, false)
	if err != nil {
		return CronExpression{}, fmt.Errorf("invalid cron minute: %w", err)
	}
	hour, err := parseCronField(parts[1], 0, 23, nil, false)
	if err != nil {
		return CronExpression{}, fmt.Errorf("invalid cron hour: %w", err)
	}
	dayOfMonth, err := parseCronField(parts[2], 1, 31, nil, false)
	if err != nil {
		return CronExpression{}, fmt.Errorf("invalid cron day of month: %w", err)
	}
	month, err := parseCronField(parts[3], 1, 12, cronMonths, false)
	if err != nil {
		return CronExpression{}, fmt.Errorf("invalid cron month: %w", err)
	}
	dayOfWeek, err := parseCronField(parts[4], 0, 7, cronWeekdays, true)
	if err != nil {
		return CronExpression{}, fmt.Errorf("invalid cron day of week: %w", err)
	}
	return CronExpression{
		minute:      minute,
		hour:        hour,
		dayOfMonth:  dayOfMonth,
		month:       month,
		dayOfWeek:   dayOfWeek,
		domWildcard: parts[2] == "*",
		dowWildcard: parts[4] == "*",
	}, nil
}

func ValidateScheduleTiming(expression, timezone string) (time.Time, error) {
	parsed, err := ParseCronExpression(expression)
	if err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	return parsed.Next(time.Now().UTC(), location)
}

// Next returns the first matching minute strictly after the supplied instant.
func (expression CronExpression) Next(after time.Time, location *time.Location) (time.Time, error) {
	if location == nil {
		return time.Time{}, fmt.Errorf("cron timezone is required")
	}
	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	deadline := candidate.AddDate(5, 0, 1)
	for !candidate.After(deadline) {
		if expression.matches(candidate) {
			return candidate.UTC(), nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron expression has no occurrence within five years")
}

func (expression CronExpression) matches(value time.Time) bool {
	if !expression.minute.contains(value.Minute()) || !expression.hour.contains(value.Hour()) || !expression.month.contains(int(value.Month())) {
		return false
	}
	domMatches := expression.dayOfMonth.contains(value.Day())
	dowMatches := expression.dayOfWeek.contains(int(value.Weekday()))
	switch {
	case expression.domWildcard && expression.dowWildcard:
		return true
	case expression.domWildcard:
		return dowMatches
	case expression.dowWildcard:
		return domMatches
	default:
		return domMatches || dowMatches
	}
}

func (field cronField) contains(value int) bool {
	if value < field.min || value > field.max || value >= len(field.allowed) {
		return false
	}
	return field.allowed[value]
}

func parseCronField(value string, minimum, maximum int, names map[string]int, normalizeSunday bool) (cronField, error) {
	field := cronField{min: minimum, max: maximum, allowed: make([]bool, maximum+1)}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return cronField{}, fmt.Errorf("empty list item")
		}
		base := item
		step := 1
		if slash := strings.IndexByte(item, '/'); slash >= 0 {
			if strings.Count(item, "/") != 1 {
				return cronField{}, fmt.Errorf("invalid step %q", item)
			}
			base = item[:slash]
			parsed, err := strconv.Atoi(item[slash+1:])
			if err != nil || parsed <= 0 {
				return cronField{}, fmt.Errorf("step must be a positive integer")
			}
			step = parsed
		}

		start, end := minimum, maximum
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				return cronField{}, fmt.Errorf("invalid range %q", base)
			}
			var err error
			start, err = parseCronValue(bounds[0], minimum, maximum, names)
			if err != nil {
				return cronField{}, err
			}
			end, err = parseCronValue(bounds[1], minimum, maximum, names)
			if err != nil {
				return cronField{}, err
			}
			if start > end {
				return cronField{}, fmt.Errorf("range start exceeds range end")
			}
		default:
			parsed, err := parseCronValue(base, minimum, maximum, names)
			if err != nil {
				return cronField{}, err
			}
			start, end = parsed, parsed
			if step != 1 {
				return cronField{}, fmt.Errorf("steps require * or a range")
			}
		}
		for candidate := start; candidate <= end; candidate += step {
			if normalizeSunday && candidate == 7 {
				field.allowed[0] = true
				continue
			}
			field.allowed[candidate] = true
		}
	}
	for candidate := minimum; candidate <= maximum; candidate++ {
		if field.allowed[candidate] {
			return field, nil
		}
	}
	return cronField{}, fmt.Errorf("field does not select any values")
}

func parseCronValue(value string, minimum, maximum int, names map[string]int) (int, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if parsed, ok := names[value]; ok {
		return parsed, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("value %q must be between %d and %d", value, minimum, maximum)
	}
	return parsed, nil
}

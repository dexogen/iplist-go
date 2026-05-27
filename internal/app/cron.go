package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronSchedule struct {
	minutes  map[int]struct{}
	hours    map[int]struct{}
	days     map[int]struct{}
	months   map[int]struct{}
	weekdays map[int]struct{}
}

func parseCronSchedule(expr string) (*cronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields")
	}
	minutes, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	hours, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	days, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day: %w", err)
	}
	months, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	weekdays, err := parseCronField(fields[4], 0, 7)
	if err != nil {
		return nil, fmt.Errorf("weekday: %w", err)
	}
	if _, ok := weekdays[7]; ok {
		delete(weekdays, 7)
		weekdays[0] = struct{}{}
	}
	return &cronSchedule{minutes: minutes, hours: hours, days: days, months: months, weekdays: weekdays}, nil
}

func parseCronField(field string, min int, max int) (map[int]struct{}, error) {
	values := map[int]struct{}{}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty list item")
		}
		step := 1
		if before, after, ok := strings.Cut(part, "/"); ok {
			part = before
			parsed, err := strconv.Atoi(after)
			if err != nil || parsed <= 0 {
				return nil, fmt.Errorf("invalid step %q", after)
			}
			step = parsed
		}

		start, end, err := parseCronRange(part, min, max)
		if err != nil {
			return nil, err
		}
		for value := start; value <= end; value += step {
			if value < min || value > max {
				return nil, fmt.Errorf("value %d outside %d-%d", value, min, max)
			}
			values[value] = struct{}{}
		}
	}
	return values, nil
}

func parseCronRange(part string, min int, max int) (int, int, error) {
	if part == "*" {
		return min, max, nil
	}
	if startRaw, endRaw, ok := strings.Cut(part, "-"); ok {
		start, err := strconv.Atoi(startRaw)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid range start %q", startRaw)
		}
		end, err := strconv.Atoi(endRaw)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid range end %q", endRaw)
		}
		if start > end {
			return 0, 0, fmt.Errorf("range start %d is greater than end %d", start, end)
		}
		return start, end, nil
	}
	value, err := strconv.Atoi(part)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid value %q", part)
	}
	return value, value, nil
}

func (s *cronSchedule) Next(after time.Time) time.Time {
	next := after.Truncate(time.Minute).Add(time.Minute)
	limit := next.AddDate(5, 0, 0)
	for !next.After(limit) {
		if s.matches(next) {
			return next
		}
		next = next.Add(time.Minute)
	}
	return time.Time{}
}

func (s *cronSchedule) matches(value time.Time) bool {
	_, minute := s.minutes[value.Minute()]
	_, hour := s.hours[value.Hour()]
	_, day := s.days[value.Day()]
	_, month := s.months[int(value.Month())]
	_, weekday := s.weekdays[int(value.Weekday())]
	return minute && hour && day && month && weekday
}

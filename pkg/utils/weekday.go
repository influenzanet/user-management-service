package utils

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

type Interval struct {
	min   int
	max   int
	entry time.Weekday
}

// Weekday assignation strategy
type Weight struct {
	total     int        // Total of cumulated weights
	intervals []Interval // Range of weight for each entry
}

type WeekDayStrategy struct {
	useWeight bool
	weights   Weight
}

var days = map[string]time.Weekday{
	"mon": time.Monday,
	"tue": time.Tuesday,
	"wed": time.Wednesday,
	"thu": time.Thursday,
	"fri": time.Friday,
	"sat": time.Saturday,
	"sun": time.Sunday,
}

// CreateWeight creates a weighted structure to handle the weighted assignation
func CreateWeight(w []int) Weight {
	var cumulated int = 0
	intervals := make([]Interval, 0)
	for entry, w := range w {
		weekday := time.Weekday(entry)
		if w > 0 {
			var min = cumulated
			cumulated = cumulated + w
			i := Interval{entry: weekday, min: min, max: cumulated - 1}
			intervals = append(intervals, i)
		}
	}
	return Weight{intervals: intervals, total: cumulated}
}

// Lookup computes the entry
// Value is expected from 0..total-1
func (w *Weight) Lookup(value int) int {
	if value < 0 {
		value = 0
	}
	var last int = 0
	for _, interval := range w.intervals {
		if value >= interval.min && value <= interval.max {
			return int(interval.entry)
		}
		last = int(interval.entry)
	}
	return last
}

func (w *Weight) String() string {
	s := make([]string, 0, len(w.intervals)+1)
	s = append(s, fmt.Sprintf("Wt=%d", w.total))
	for _, i := range w.intervals {
		lab := fmt.Sprintf("%s=[%d,%d]", i.entry.String(), i.min, i.max)
		s = append(s, lab)
	}
	return strings.Join(s, ", ")
}

func parseTuple(str string) (string, string) {
	r := strings.Split(str, "=")
	if len(r) < 2 {
		return "", ""
	}
	return strings.ToLower(strings.TrimSpace(r[0])), strings.TrimSpace(r[1])
}

// ParseWeeklyWeight parse a string attributing a weight for each day of the week
// Expected format is a comma separated assignation with [DayName]=Weight with DayName = Mon,Tue,Web,Thu,Frid,Sat,Sun (case insensitive)
// Weight is a positive integer value or Zero
func ParseWeeklyWeight(str string) ([]int, error) {
	weights := make([]int, 7)
	wdays := strings.Split(str, ",")
	for idx, d := range wdays {
		name, value := parseTuple(d)
		if name == "" {
			return weights, fmt.Errorf("expecting day name for entry %d", idx)
		}
		dayIndex, ok := days[name]
		if !ok {
			return weights, fmt.Errorf("invalid day name '%s' for entry %d ", name, idx)
		}
		w, err := strconv.Atoi(value)
		if err != nil {
			return weights, fmt.Errorf("invalid day weight value '%s' for entry %d, expecting integer : %s", value, idx, err)
		}
		if w < 0 {
			return weights, fmt.Errorf("invalid day weight for entry %d, expecting positive integer : %s", idx, err)
		}
		weights[dayIndex] = w
	}
	return weights, nil
}

func CreateWeekdayWeightedStrategy(weights []int) WeekDayStrategy {
	return WeekDayStrategy{useWeight: true, weights: CreateWeight(weights)}
}

func CreateWeekdayDefaultStrategy() WeekDayStrategy {
	return WeekDayStrategy{useWeight: false}
}

// Assign a new weekday using the assignation strategy
func (s *WeekDayStrategy) Weekday() int {
	var weekday int
	if s.useWeight {
		value := rand.Intn(s.weights.total)
		weekday = s.weights.Lookup(value)
		fmt.Printf(" %d=>%d ", value, weekday)
	} else {
		weekday = rand.Intn(7)
	}
	return weekday
}

func (s *WeekDayStrategy) String() string {
	if s.useWeight {
		return "Weighted strategy : " + s.weights.String()
	}
	return "Random strategy"
}

package cacheproxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// From is inclusive and To exclusive, both in Unix milliseconds.
// Sorting uses immutable arrival identity; completion updates cannot move a row.
type RequestQuery struct {
	From, To             int64
	Cursor               string
	Sort, Direction      string
	Limit                int
	HideAdjacentCanceled bool
}

type requestCursor struct {
	Sort      string `json:"sort"`
	Direction string `json:"direction"`
	At        int64  `json:"at"`
	ID        int64  `json:"id"`
}

func (q RequestQuery) sorting() (string, string) {
	sort, direction := q.Sort, q.Direction
	if sort == "" {
		sort = "time"
	}
	if direction == "" {
		direction = "descending"
	}
	return sort, direction
}

func (q RequestQuery) cursor() (requestCursor, error) {
	sort, direction := q.sorting()
	c := requestCursor{Sort: sort, Direction: direction}
	if q.Cursor == "" {
		return c, nil
	}
	invalid := errors.New("invalid request cursor")
	if len(q.Cursor) > 1024 {
		return c, invalid
	}
	// Existing time-descending links remain valid.
	if parts := strings.Split(q.Cursor, ":"); len(parts) == 2 && sort == "time" && direction == "descending" {
		var e1, e2 error
		c.At, e1 = strconv.ParseInt(parts[0], 10, 64)
		c.ID, e2 = strconv.ParseInt(parts[1], 10, 64)
		if e1 != nil || e2 != nil {
			return c, invalid
		}
	} else {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		c = requestCursor{}
		if err != nil || json.Unmarshal(raw, &c) != nil {
			return c, invalid
		}
	}
	if c.Sort != sort || c.Direction != direction || c.At < 0 || c.ID <= 0 {
		return c, invalid
	}
	return c, nil
}

func (q RequestQuery) Validate() error {
	sort, direction := q.sorting()
	if q.Limit < 1 || q.Limit > MaxRecentRequests || q.From < 0 || q.To < 0 || (q.To != 0 && q.To <= q.From) {
		return errors.New("invalid request range or limit")
	}
	if sort != "time" || (direction != "ascending" && direction != "descending") {
		return errors.New("invalid request sort")
	}
	_, err := q.cursor()
	return err
}

func (q RequestQuery) order() string {
	_, direction := q.sorting()
	dir := " DESC"
	if direction == "ascending" {
		dir = " ASC"
	}
	return "requested_at" + dir + ",event_id" + dir
}

func (q RequestQuery) nextCursor(last RecentRequest) string {
	sort, direction := q.sorting()
	c := requestCursor{Sort: sort, Direction: direction, At: last.At, ID: last.ID}
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (q RequestQuery) conditions(getOnly bool) ([]string, []any) {
	var conditions []string
	var args []any
	if getOnly {
		conditions = append(conditions, "method='GET'")
	}
	if getOnly && q.HideAdjacentCanceled {
		// Match the next GET by arrival order before applying range, sort or
		// pagination. Only completed video responses can hide a canceled row;
		// an intervening cancellation, failure or different resource breaks it.
		conditions = append(conditions, `NOT (outcome='canceled' AND resource_md5 IS NOT NULL AND COALESCE((
 SELECT next.outcome='completed' AND next.status IN (200,206)
   AND next.resource_md5=request_events.resource_md5
   AND (next.song_id IS NULL OR request_events.song_id IS NULL OR next.song_id=request_events.song_id)
 FROM request_events AS next
 WHERE next.method='GET'
   AND (next.requested_at,next.event_id)>(request_events.requested_at,request_events.event_id)
   AND next.requested_at<=request_events.requested_at+1000
 ORDER BY next.requested_at,next.event_id LIMIT 1
),0))`)
	}
	if q.From > 0 {
		conditions = append(conditions, "requested_at>=?")
		args = append(args, q.From)
	}
	if q.To > 0 {
		conditions = append(conditions, "requested_at<?")
		args = append(args, q.To)
	}
	return conditions, args
}

func (q RequestQuery) window(getOnly bool) (string, []any) {
	conditions, args := q.conditions(getOnly)
	if q.Cursor != "" {
		c, _ := q.cursor() // Validate is called before building the query.
		_, direction := q.sorting()
		op := "<"
		if direction == "ascending" {
			op = ">"
		}
		conditions = append(conditions, "(requested_at,event_id)"+op+"(?,?)")
		args = append(args, c.At, c.ID)
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	return "SELECT * FROM request_events" + where + " ORDER BY " + q.order() + " LIMIT ?", append(args, q.Limit+1)
}

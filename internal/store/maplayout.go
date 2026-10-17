package store

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// Limits of a stored map layout.
const (
	maxMapNodes    = 300
	maxMapItems    = 100
	maxMapText     = 200
	maxMapCoord    = 100000
	minFrameSize   = 40
	maxFrameSize   = 20000
	mapLayoutMetaK = "map_layout"
)

var (
	mapColors  = map[string]bool{"neutral": true, "blue": true, "green": true, "orange": true, "red": true, "violet": true}
	mapSizes   = map[string]bool{"S": true, "M": true, "L": true}
	mapNodeKey = regexp.MustCompile(`^(in|eg):[0-9]+$`)
)

// MapPoint is the position of a port on the map, in map pixels.
type MapPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// MapItem is an annotation the operator draws under the ports and links:
// a text label or a frame.
type MapItem struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // "text" or "frame"
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w,omitempty"` // frames only
	H     int    `json:"h,omitempty"` // frames only
	Text  string `json:"text"`
	Color string `json:"color,omitempty"`
	Size  string `json:"size,omitempty"` // texts only: S, M, L
}

// MapLayout is how the operator arranged the route map. Node keys are
// "in:<ingress id>" and "eg:<endpoint id>". It is shared by every admin and
// is not part of what agents receive, so saving it does not bump the config
// version.
type MapLayout struct {
	Nodes map[string]MapPoint `json:"nodes"`
	Items []MapItem           `json:"items"`
}

// normalize validates the layout and fills defaults in place.
func (l *MapLayout) normalize() error {
	if l.Nodes == nil {
		l.Nodes = map[string]MapPoint{}
	}
	if l.Items == nil {
		l.Items = []MapItem{}
	}
	if len(l.Nodes) > maxMapNodes {
		return invalidf("map: too many positioned ports (%d > %d)", len(l.Nodes), maxMapNodes)
	}
	for key, p := range l.Nodes {
		if !mapNodeKey.MatchString(key) {
			return invalidf("map: bad port key %q", key)
		}
		if !coordOK(p.X) || !coordOK(p.Y) {
			return invalidf("map: position of %s is out of range", key)
		}
	}
	if len(l.Items) > maxMapItems {
		return invalidf("map: too many annotations (%d > %d)", len(l.Items), maxMapItems)
	}
	seen := map[string]bool{}
	for i := range l.Items {
		it := &l.Items[i]
		if it.ID == "" || len(it.ID) > 40 || seen[it.ID] {
			return invalidf("map: annotation %d has a bad or duplicate id", i+1)
		}
		seen[it.ID] = true
		if it.Kind != "text" && it.Kind != "frame" {
			return invalidf("map: annotation %q has unknown kind %q", it.ID, it.Kind)
		}
		if !coordOK(it.X) || !coordOK(it.Y) {
			return invalidf("map: annotation %q is out of range", it.ID)
		}
		if len(it.Text) > maxMapText {
			return invalidf("map: annotation text too long (max %d)", maxMapText)
		}
		if it.Color == "" {
			it.Color = "neutral"
		}
		if !mapColors[it.Color] {
			return invalidf("map: annotation %q has unknown color %q", it.ID, it.Color)
		}
		if it.Kind == "frame" {
			it.Size = ""
			if it.W < minFrameSize || it.W > maxFrameSize || it.H < minFrameSize || it.H > maxFrameSize {
				return invalidf("map: frame %q must be %d-%d px wide and high", it.ID, minFrameSize, maxFrameSize)
			}
			continue
		}
		it.W, it.H = 0, 0
		if it.Size == "" {
			it.Size = "M"
		}
		if !mapSizes[it.Size] {
			return invalidf("map: text %q has unknown size %q", it.ID, it.Size)
		}
	}
	return nil
}

func coordOK(v int) bool { return v >= -maxMapCoord && v <= maxMapCoord }

// remap rewrites node keys after entities were re-created with new ids
// (a restore). Positions of ports that no longer exist are dropped.
func (l *MapLayout) remap(inports, endpoints map[int64]int64) {
	nodes := make(map[string]MapPoint, len(l.Nodes))
	for key, p := range l.Nodes {
		kind, idStr, _ := strings.Cut(key, ":")
		old, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			continue
		}
		table := inports
		if kind == "eg" {
			table = endpoints
		}
		if id, ok := table[old]; ok {
			nodes[kind+":"+strconv.FormatInt(id, 10)] = p
		}
	}
	l.Nodes = nodes
}

// GetMapLayout returns the saved map layout (empty when none was saved).
func (s *Store) GetMapLayout() (MapLayout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return getMap(s.cfg)
}

func getMap(q querier) (MapLayout, error) {
	var raw string
	err := q.QueryRowContext(bg, `SELECT v FROM meta WHERE k = ?`, mapLayoutMetaK).Scan(&raw)
	l := MapLayout{}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &l)
	} else if isNoRows(err) {
		err = nil
	}
	if err != nil {
		return MapLayout{}, err
	}
	if err := l.normalize(); err != nil {
		return MapLayout{}, err
	}
	return l, nil
}

// SaveMapLayout validates and stores the map layout.
func (s *Store) SaveMapLayout(l MapLayout) error {
	if err := l.normalize(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := putMap(s.cfg, l); err != nil {
		return err
	}
	return s.cfg.save()
}

func putMap(q querier, l MapLayout) error {
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(bg, `INSERT OR REPLACE INTO meta (k, v) VALUES (?, ?)`, mapLayoutMetaK, string(raw))
	return err
}

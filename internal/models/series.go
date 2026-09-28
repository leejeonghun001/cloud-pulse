package models

// Resolutions supported for series queries, in seconds.
const (
	ResolutionRaw = 15
	Resolution5m  = 300
	Resolution1h  = 3600
)

const (
	sixHours = 6 * 3600
	sevenDay = 7 * 24 * 3600
)

// ResolutionFor picks the storage tier resolution (seconds) for a query
// spanning [from, to]: raw (15s) for ranges up to 6h, 5m for ranges up to
// 7d, and 1h otherwise.
func ResolutionFor(from, to int64) int {
	span := to - from
	switch {
	case span <= sixHours:
		return ResolutionRaw
	case span <= sevenDay:
		return Resolution5m
	default:
		return Resolution1h
	}
}

// Series is a column-oriented (uPlot-friendly) set of metric points for a
// host over [From, To]. All slices share the same length as Timestamps and
// are encoded as [] rather than null when empty.
type Series struct {
	HostID     string    `json:"host_id"`
	Resolution int       `json:"resolution"`
	From       int64     `json:"from"`
	To         int64     `json:"to"`
	Timestamps []int64   `json:"ts"`
	CPU        []float64 `json:"cpu"`
	Mem        []float64 `json:"mem"`
	Disk       []float64 `json:"disk"`
	NetRx      []float64 `json:"net_rx"`
	NetTx      []float64 `json:"net_tx"`
	DiskRead   []float64 `json:"disk_read"`
	DiskWrite  []float64 `json:"disk_write"`
	Load1      []float64 `json:"load1"`
}

// SeriesPoint is a single row of series data, used with Series.Append.
type SeriesPoint struct {
	TS        int64
	CPU       float64
	Mem       float64
	Disk      float64
	NetRx     float64
	NetTx     float64
	DiskRead  float64
	DiskWrite float64
	Load1     float64
}

// NewSeries constructs an empty Series for hostID/resolution/[from,to] with
// all slices initialized to non-nil, empty slices.
func NewSeries(hostID string, resolution int, from, to int64) Series {
	return Series{
		HostID:     hostID,
		Resolution: resolution,
		From:       from,
		To:         to,
		Timestamps: []int64{},
		CPU:        []float64{},
		Mem:        []float64{},
		Disk:       []float64{},
		NetRx:      []float64{},
		NetTx:      []float64{},
		DiskRead:   []float64{},
		DiskWrite:  []float64{},
		Load1:      []float64{},
	}
}

// Append adds p's values to the series' parallel columns.
func (s *Series) Append(p SeriesPoint) {
	s.Timestamps = append(s.Timestamps, p.TS)
	s.CPU = append(s.CPU, p.CPU)
	s.Mem = append(s.Mem, p.Mem)
	s.Disk = append(s.Disk, p.Disk)
	s.NetRx = append(s.NetRx, p.NetRx)
	s.NetTx = append(s.NetTx, p.NetTx)
	s.DiskRead = append(s.DiskRead, p.DiskRead)
	s.DiskWrite = append(s.DiskWrite, p.DiskWrite)
	s.Load1 = append(s.Load1, p.Load1)
}

// Len returns the number of points in the series (the length of
// Timestamps).
func (s Series) Len() int {
	return len(s.Timestamps)
}

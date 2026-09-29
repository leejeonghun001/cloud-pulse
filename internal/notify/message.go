package notify

// Severity classifies a Message for color/icon selection by a Sender.
type Severity string

// Supported severities.
const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
	SeverityResolved Severity = "resolved"
)

// Field is one label/value pair rendered alongside a Message (e.g. an
// embed field on Discord, an extra line of text on platforms without
// structured fields).
type Field struct {
	Name  string
	Value string
}

// Message is the platform-agnostic notification a Sender delivers.
// Image, when non-empty, is a PNG chart rendered by
// internal/alerting/chart; ImageName is the filename Senders should
// use for it (e.g. "chart.png").
type Message struct {
	Title    string
	Text     string
	Severity Severity
	Fields   []Field
	// URL is an optional dashboard link (e.g. to the host that fired
	// the alert).
	URL string
	// Image is optional PNG-encoded chart bytes.
	Image []byte
	// ImageName is the filename associated with Image (e.g.
	// "chart.png"); ignored when Image is empty.
	ImageName string
}

// HasImage reports whether m carries chart image bytes.
func (m Message) HasImage() bool {
	return len(m.Image) > 0
}

package types

type Status int

const (
	Placed       Status = iota
	Taken        Status = iota
	Scanned      Status = iota
	Disconnected Status = iota
)

func (s Status) String() string {
	switch s {
	case Placed:
		return "placed the computer"
	case Taken:
		return "taken the computer"
	case Scanned:
		return "scanned new tag"
	case Disconnected:
		return "arduino with RFID reader disconnected"
	default:
		return "unknown status"
	}
}

type MQTTMessage struct {
	Message string `json:"message"`
	RFID    string `json:"RFID"`
	Slots   string `json:"slots"`
	Status  Status `json:"status"`
}

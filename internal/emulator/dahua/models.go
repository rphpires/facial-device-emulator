package dahua

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// flexInt accepts an integer either as a JSON number or as a JSON string.
// Some clients send "UserID": "1" to FaceInfoManager, and a plain int would
// reject the whole payload, dropping the face.
type flexInt int

func (f *flexInt) UnmarshalJSON(data []byte) error {
	text := string(data)
	if text == "null" {
		return nil
	}

	if len(text) > 1 && text[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if s == "" {
			return nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("UserID %q is not an integer: %w", s, err)
		}
		*f = flexInt(n)
		return nil
	}

	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

// flexStrings accepts either a list of strings or a single string. Dahua
// documents PhotoData as a list, but clients send both forms.
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(data []byte) error {
	text := string(data)
	if text == "null" {
		return nil
	}

	if len(text) > 1 && text[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = flexStrings{s}
		return nil
	}

	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*f = flexStrings(list)
	return nil
}

// Card is a card record on a Dahua device.
type Card struct {
	RecNo          int       `db:"rec_no"`
	CardName       string    `db:"card_name"`
	UserID         int       `db:"user_id"`
	CardNo         string    `db:"card_no"`
	ValidDateStart time.Time `db:"valid_date_start"`
	ValidDateEnd   time.Time `db:"valid_date_end"`
}

// Face is a face record on a Dahua device.
type Face struct {
	UserID int    `db:"user_id"`
	MD5    string `db:"md5"`
}

// CountItems holds counters for the different item types.
type CountItems struct {
	Cards int
	Faces int
}

// Event is a Dahua event envelope.
type Event struct {
	Events []EventData `json:"Events"`
	Time   string      `json:"Time"`
}

// EventData is a single Dahua event.
type EventData struct {
	Action          string           `json:"Action"`
	Code            string           `json:"Code"`
	Data            EventDataDetails `json:"Data"`
	Index           int              `json:"Index"`
	PhysicalAddress string           `json:"PhysicalAddress"`
}

// EventDataDetails holds the event-specific fields (same format as a real Dahua device).
type EventDataDetails struct {
	Alive      int    `json:"Alive,omitempty"`
	BlockId    int    `json:"BlockId,omitempty"`
	CardName   string `json:"CardName,omitempty"`
	CardNo     string `json:"CardNo,omitempty"`
	CardStatus int    `json:"CardStatus,omitempty"`
	CardType   int    `json:"CardType"`
	// CollectResult only exists on the fingerprint enroll event, triggered by
	// accessControl.cgi?action=captureFingerprint. The client diverts the
	// event as soon as it sees this key. It is a pointer so a failed capture
	// (false) is not dropped by omitempty.
	CollectResult *bool `json:"CollectResult,omitempty"`
	CreateTime    int64 `json:"CreateTime,omitempty"`
	Door          int   `json:"Door"`
	ErrorCode     int   `json:"ErrorCode"`
	EventGroupID  int   `json:"EventGroupID,omitempty"`
	FaceIndex     int   `json:"FaceIndex,omitempty"`
	FeatureId     int   `json:"FeatureId,omitempty"`
	// Fingerprint carries the template captured in the enroll event.
	Fingerprint      string            `json:"Fingerprint,omitempty"`
	HatColor         string            `json:"HatColor,omitempty"`
	HatType          int               `json:"HatType,omitempty"`
	ImageInfo        []ImageInfo       `json:"ImageInfo,omitempty"`
	Method           int               `json:"Method"`
	ObjectProperties *ObjectProperties `json:"ObjectProperties,omitempty"`
	// QRCodeStr accompanies events with Method=14 (QRCODE) and feeds the
	// client's TOTP validation.
	QRCodeStr  string `json:"QRCodeStr,omitempty"`
	ReaderID   string `json:"ReaderID"`
	RealUTC    int64  `json:"RealUTC,omitempty"`
	Similarity int    `json:"Similarity,omitempty"`
	SnapPath   string `json:"SnapPath,omitempty"`
	Status     int    `json:"Status"`
	// TrafficCar is the LPR block. Clients check TrafficCar.PlateNumber
	// before anything else when extracting the credential from an event.
	TrafficCar *TrafficCar `json:"TrafficCar,omitempty"`
	Type       string      `json:"Type"`
	UTC        int64       `json:"UTC"`
	UserID     string      `json:"UserID"`
	UserType   int         `json:"UserType"`
}

// TrafficCar carries the plate read in LPR events.
type TrafficCar struct {
	PlateNumber string `json:"PlateNumber"`
}

// ObjectProperties describes the detected object.
type ObjectProperties struct {
	HatInfo *HatInfo `json:"HatInfo,omitempty"`
}

// HatInfo describes a detected hat/cap.
type HatInfo struct {
	HatColor string `json:"HatColor"`
	HatType  int    `json:"HatType"`
}

// ImageInfo describes the event image.
type ImageInfo struct {
	Height int `json:"Height"`
	Length int `json:"Length"`
	Offset int `json:"Offset"`
	Type   int `json:"Type"`
	Width  int `json:"Width"`
}

// OnlineEvent is the full event pushed to the remote server (same format as a real Dahua device).
type OnlineEvent struct {
	Channel  int         `json:"Channel"`
	Events   []EventData `json:"Events"`
	FilePath string      `json:"FilePath"`
	Time     string      `json:"Time"`
}

// FindFaceResponse is the response of a face search.
type FindFaceResponse struct {
	Token int `json:"Token"`
	Total int `json:"Total"`
}

// GetFaceResponse is the response of a face fetch.
type GetFaceResponse struct {
	Info []FaceInfo `json:"Info"`
}

// FaceInfo describes a face.
type FaceInfo struct {
	MD5    string `json:"MD5"`
	UserID int    `json:"UserID"`
}

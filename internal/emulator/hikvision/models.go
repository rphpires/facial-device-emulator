package hikvision

import "time"

// User is a user stored on the Hikvision device.
type User struct {
	EmployeeNo   string    `db:"employee_no"`
	Name         string    `db:"name"`
	Password     string    `db:"password"`
	LocalUIRight string    `db:"local_ui_right"`
	BeginTime    time.Time `db:"begin_time"`
	EndTime      time.Time `db:"end_time"`
}

// Card is a card stored on the Hikvision device.
type Card struct {
	EmployeeNo string `db:"employee_no"`
	CardNo     string `db:"card_no"`
}

// Face is a face record stored on the Hikvision device.
type Face struct {
	UserID    int    `db:"user_id"`
	PhotoData string `db:"photo_data"`
}

// Finger is a fingerprint stored on the Hikvision device.
type Finger struct {
	EmployeeNo string `db:"employee_no"`
	DataIndex  int    `db:"data_index"`
	Template   string `db:"template"`
}

// CountItems holds the counters for each item type.
type CountItems struct {
	Users        int
	Cards        int
	Faces        int
	Fingerprints int
}

// UserSearchResponse is the user search response.
type UserSearchResponse struct {
	UserInfoSearch struct {
		SearchID           string     `json:"searchID"`
		ResponseStatusStrg string     `json:"responseStatusStrg"`
		NumOfMatches       int        `json:"numOfMatches"`
		TotalMatches       int        `json:"totalMatches"`
		UserInfo           []UserInfo `json:"UserInfo"`
	} `json:"UserInfoSearch"`
}

// UserInfo is a user entry in a search response.
type UserInfo struct {
	EmployeeNo         string `json:"employeeNo"`
	Name               string `json:"name"`
	UserType           string `json:"userType"`
	SortByNamePosition int    `json:"sortByNamePosition"`
	SortByNameFlag     string `json:"sortByNameFlag"`
	CloseDelayEnabled  bool   `json:"closeDelayEnabled"`
	Valid              struct {
		Enable    bool   `json:"enable"`
		BeginTime string `json:"beginTime"`
		EndTime   string `json:"endTime"`
		TimeType  string `json:"timeType"`
	} `json:"Valid"`
	BelongGroup string `json:"belongGroup"`
	Password    string `json:"password"`
	DoorRight   string `json:"doorRight"`
	RightPlan   []struct {
		DoorNo         int    `json:"doorNo"`
		PlanTemplateNo string `json:"planTemplateNo"`
	} `json:"RightPlan"`
	MaxOpenDoorTime    int    `json:"maxOpenDoorTime"`
	OpenDoorTime       int    `json:"openDoorTime"`
	RoomNumber         int    `json:"roomNumber"`
	FloorNumber        int    `json:"floorNumber"`
	LocalUIRight       bool   `json:"localUIRight"`
	Gender             string `json:"gender"`
	NumOfCard          int    `json:"numOfCard"`
	NumOfRemoteControl int    `json:"numOfRemoteControl"`
	NumOfFP            int    `json:"numOfFP"`
	NumOfFace          int    `json:"numOfFace"`
	PersonInfoExtends  []struct {
		Value string `json:"value"`
	} `json:"PersonInfoExtends"`
}

// CardSearchResponse is the card search response.
type CardSearchResponse struct {
	CardInfoSearch struct {
		SearchID           string     `json:"searchID"`
		ResponseStatusStrg string     `json:"responseStatusStrg"`
		NumOfMatches       int        `json:"numOfMatches"`
		TotalMatches       int        `json:"totalMatches"`
		CardInfo           []CardInfo `json:"CardInfo"`
	} `json:"CardInfoSearch"`
}

// CardInfo is a card entry in a search response.
type CardInfo struct {
	EmployeeNo               string `json:"employeeNo"`
	CardNo                   string `json:"cardNo"`
	IsCardAsRemoteControlBtn bool   `json:"isCardAsRemoteControlBtn"`
	LeaderCard               string `json:"leaderCard"`
	CardType                 string `json:"cardType"`
}

// FaceSearchResponse is the face search response.
type FaceSearchResponse struct {
	SearchID           string     `json:"searchID"`
	ResponseStatusStrg string     `json:"responseStatusStrg"`
	NumOfMatches       int        `json:"numOfMatches"`
	TotalMatches       int        `json:"totalMatches"`
	MatchList          []FaceInfo `json:"MatchList"`
}

// FaceInfo is a face entry in a search response.
type FaceInfo struct {
	FPID      string `json:"FPID"`
	FaceURL   string `json:"faceURL"`
	ModelData string `json:"modelData"`
}

// Event is a Hikvision access-control event.
type Event struct {
	IPAddress             string `json:"ipAddress"`
	IPv6Address           string `json:"ipv6Address"`
	PortNo                int    `json:"portNo"`
	Protocol              string `json:"protocol"`
	MacAddress            string `json:"macAddress"`
	ChannelID             int    `json:"channelID"`
	DateTime              string `json:"dateTime"`
	ActivePostCount       int    `json:"activePostCount"`
	EventType             string `json:"eventType"`
	EventState            string `json:"eventState"`
	EventDescription      string `json:"eventDescription"`
	AccessControllerEvent struct {
		DeviceName     string `json:"deviceName"`
		MajorEventType int    `json:"majorEventType"`
		SubEventType   int    `json:"subEventType"`
		CardNo         string `json:"cardNo"`
		CardType       int    `json:"cardType"`
		Name           string `json:"name"`
		CardReaderKind int    `json:"cardReaderKind"`
		CardReaderNo   int    `json:"cardReaderNo"`
		// DoorNo accompanies door events. Clients read cardReaderNo and fall
		// back to doorNo when the former is zero.
		DoorNo            int    `json:"doorNo,omitempty"`
		VerifyNo          int    `json:"verifyNo"`
		EmployeeNoString  string `json:"employeeNoString"`
		SerialNo          int    `json:"serialNo"`
		UserType          string `json:"userType"`
		CurrentVerifyMode string `json:"currentVerifyMode"`
		CurrentEvent      bool   `json:"currentEvent"`
		// FrontSerialNo/StatusValue are pointers so they can be truly omitted
		// (nil) in a remote-check request event: in online mode the real device
		// sends the event WITHOUT frontSerialNo and statusValue (undecided).
		// A missing frontSerialNo is exactly what makes the server take the
		// remote-check path. See handleRemoteCheck.
		FrontSerialNo    *int   `json:"frontSerialNo,omitempty"`
		AttendanceStatus string `json:"attendanceStatus"`
		Label            string `json:"label"`
		StatusValue      *int   `json:"statusValue,omitempty"`
		// RemoteCheck marks the event as a remote verification request (device
		// undecided, waiting for PUT /ISAPI/AccessControl/remoteCheck).
		RemoteCheck         bool   `json:"remoteCheck,omitempty"`
		Mask                string `json:"mask"`
		Helmet              string `json:"helmet"`
		PicturesNumber      int    `json:"picturesNumber"`
		PurePwdVerifyEnable bool   `json:"purePwdVerifyEnable"`
		FaceRect            struct {
			Height float64 `json:"height"`
			Width  float64 `json:"width"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
		} `json:"FaceRect"`
		UnlockRoomNo string `json:"unlockRoomNo"`
	} `json:"AccessControllerEvent"`
}

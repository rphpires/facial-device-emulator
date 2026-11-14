package hikvision

// User handlers: UserInfo count/search/record/modify and UserInfoDetail
// delete.

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func (e *Emulator) handleGetUserCount(c *gin.Context) {
	counts, err := e.repo.CountItems()
	if err != nil {
		c.XML(http.StatusInternalServerError, errorXMLResponse(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"UserInfoCount": gin.H{
			"userNumber":                counts.Users,
			"bindFaceUserNumber":        counts.Faces,
			"bindFingerprintUserNumber": counts.Fingerprints,
			"bindCardUserNumber":        counts.Cards,
			"bindRemoteControlNumber":   0,
		},
	})
}

func (e *Emulator) handlePostUserSearch(c *gin.Context) {
	var payload struct {
		UserInfoSearchCond struct {
			MaxResults           int `json:"maxResults"`
			SearchResultPosition int `json:"searchResultPosition"`
		} `json:"UserInfoSearchCond"`
	}

	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	users, err := e.repo.GetUsers(payload.UserInfoSearchCond.MaxResults, payload.UserInfoSearchCond.SearchResultPosition)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Convert to response format
	var userInfos []UserInfo
	for _, user := range users {
		localUIRightBool := user.LocalUIRight != "" && user.LocalUIRight != "0"

		userInfo := UserInfo{
			EmployeeNo:         user.EmployeeNo,
			Name:               user.Name,
			UserType:           "normal",
			SortByNamePosition: 0,
			SortByNameFlag:     "#",
			CloseDelayEnabled:  false,
			Valid: struct {
				Enable    bool   `json:"enable"`
				BeginTime string `json:"beginTime"`
				EndTime   string `json:"endTime"`
				TimeType  string `json:"timeType"`
			}{
				Enable:    true,
				BeginTime: user.BeginTime.Format("2006-01-02T15:04:05"),
				EndTime:   user.EndTime.Format("2006-01-02T15:04:05"),
				TimeType:  "local",
			},
			BelongGroup: "",
			Password:    user.Password,
			DoorRight:   "1",
			RightPlan: []struct {
				DoorNo         int    `json:"doorNo"`
				PlanTemplateNo string `json:"planTemplateNo"`
			}{
				{DoorNo: 1, PlanTemplateNo: "1"},
			},
			MaxOpenDoorTime:    0,
			OpenDoorTime:       0,
			RoomNumber:         0,
			FloorNumber:        0,
			LocalUIRight:       localUIRightBool,
			Gender:             "unknown",
			NumOfCard:          1,
			NumOfRemoteControl: 0,
			NumOfFP:            0,
			NumOfFace:          0,
			PersonInfoExtends: []struct {
				Value string `json:"value"`
			}{
				{Value: ""},
			},
		}
		userInfos = append(userInfos, userInfo)
	}

	// Get total users
	counts, _ := e.repo.CountItems()

	c.JSON(http.StatusOK, UserSearchResponse{
		UserInfoSearch: struct {
			SearchID           string     `json:"searchID"`
			ResponseStatusStrg string     `json:"responseStatusStrg"`
			NumOfMatches       int        `json:"numOfMatches"`
			TotalMatches       int        `json:"totalMatches"`
			UserInfo           []UserInfo `json:"UserInfo"`
		}{
			SearchID:           "1",
			ResponseStatusStrg: "MORE",
			NumOfMatches:       len(userInfos),
			TotalMatches:       counts.Users,
			UserInfo:           userInfos,
		},
	})
}

func (e *Emulator) handlePostUserRecord(c *gin.Context) {
	var payload struct {
		UserInfo struct {
			EmployeeNo   string `json:"employeeNo"`
			Name         string `json:"name"`
			Password     string `json:"password"`
			LocalUIRight bool   `json:"localUIRight"`
			Valid        struct {
				BeginTime string `json:"beginTime"`
				EndTime   string `json:"endTime"`
			} `json:"Valid"`
		} `json:"UserInfo"`
	}

	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check whether it already exists
	exists, err := e.repo.CheckIfUserExists(payload.UserInfo.EmployeeNo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if exists {
		c.JSON(http.StatusBadRequest, gin.H{
			"statusCode":    6,
			"statusString":  "Invalid Content",
			"subStatusCode": "employeeNoAlreadyExist",
			"errorCode":     1610637344,
			"errorMsg":      "checkUser",
		})
		return
	}

	// Convert to internal structure
	localUIRight := "0"
	if payload.UserInfo.LocalUIRight {
		localUIRight = "1"
	}

	beginTime, _ := time.Parse("2006-01-02T15:04:05", payload.UserInfo.Valid.BeginTime)
	endTime, _ := time.Parse("2006-01-02T15:04:05", payload.UserInfo.Valid.EndTime)

	user := &User{
		EmployeeNo:   payload.UserInfo.EmployeeNo,
		Name:         payload.UserInfo.Name,
		Password:     payload.UserInfo.Password,
		LocalUIRight: localUIRight,
		BeginTime:    beginTime,
		EndTime:      endTime,
	}

	if err := e.repo.AddUser(user); err != nil {
		e.tracer.Error("[USER_SYNC] Failed to add user: employeeNo=%s, name=%s, error=%v", user.EmployeeNo, user.Name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Get updated total
	totalUsers, _ := e.repo.GetTotalUsers()
	e.tracer.Info("[USER_SYNC] User added successfully: employeeNo=%s, name=%s, total_users=%d", user.EmployeeNo, user.Name, totalUsers)

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
	})
}

func (e *Emulator) handlePutUserModify(c *gin.Context) {
	var payload struct {
		UserInfo struct {
			EmployeeNo   string `json:"employeeNo"`
			Name         string `json:"name"`
			Password     string `json:"password"`
			LocalUIRight bool   `json:"localUIRight"`
			Valid        struct {
				BeginTime string `json:"beginTime"`
				EndTime   string `json:"endTime"`
			} `json:"Valid"`
		} `json:"UserInfo"`
	}

	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Convert to internal structure
	localUIRight := "0"
	if payload.UserInfo.LocalUIRight {
		localUIRight = "1"
	}

	beginTime, _ := time.Parse("2006-01-02T15:04:05", payload.UserInfo.Valid.BeginTime)
	endTime, _ := time.Parse("2006-01-02T15:04:05", payload.UserInfo.Valid.EndTime)

	user := &User{
		EmployeeNo:   payload.UserInfo.EmployeeNo,
		Name:         payload.UserInfo.Name,
		Password:     payload.UserInfo.Password,
		LocalUIRight: localUIRight,
		BeginTime:    beginTime,
		EndTime:      endTime,
	}

	if err := e.repo.UpdateUser(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
	})
}

func (e *Emulator) handleGetUserDeleteProcess(c *gin.Context) {
	status := "success"
	if e.deleteInProgress.Load() {
		status = "inProgress"
	}

	c.JSON(http.StatusOK, gin.H{
		"UserInfoDetailDeleteProcess": gin.H{
			"status": status,
		},
	})
}

func (e *Emulator) handlePutUserDelete(c *gin.Context) {
	var payload struct {
		UserInfoDetail struct {
			Mode           string `json:"mode"`
			EmployeeNoList []struct {
				EmployeeNo string `json:"employeeNo"`
			} `json:"EmployeeNoList"`
		} `json:"UserInfoDetail"`
	}

	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if payload.UserInfoDetail.Mode != "byEmployeeNo" {
		c.JSON(http.StatusBadRequest, gin.H{
			"statusCode":    77,
			"statusString":  "Error",
			"subStatusCode": "Emulator Invalid Mode",
		})
		return
	}

	// Mark the delete process as started
	e.deleteInProgress.Store(true)
	defer e.deleteInProgress.Store(false)

	// Delete users
	for _, emp := range payload.UserInfoDetail.EmployeeNoList {
		e.tracer.Info("Deleting user with EmployeeNo= %s", emp.EmployeeNo)

		if err := e.repo.DeleteUser(emp.EmployeeNo); err != nil {
			e.tracer.Error("Failed to delete user %s: %v", emp.EmployeeNo, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
	})
}

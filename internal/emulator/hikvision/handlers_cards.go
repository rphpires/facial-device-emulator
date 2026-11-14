package hikvision

// Card handlers: CardInfo count/search/record/delete.

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (e *Emulator) handleGetCardCount(c *gin.Context) {
	counts, err := e.repo.CountItems()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"CardInfoCount": gin.H{
			"cardNumber": counts.Cards,
		},
	})
}

func (e *Emulator) handlePostCardSearch(c *gin.Context) {
	var payload struct {
		CardInfoSearchCond struct {
			MaxResults           int `json:"maxResults"`
			SearchResultPosition int `json:"searchResultPosition"`
		} `json:"CardInfoSearchCond"`
	}

	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cards, err := e.repo.GetCards(payload.CardInfoSearchCond.MaxResults, payload.CardInfoSearchCond.SearchResultPosition)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Convert to response format
	var cardInfos []CardInfo
	for _, card := range cards {
		cardInfo := CardInfo{
			EmployeeNo:               card.EmployeeNo,
			CardNo:                   card.CardNo,
			IsCardAsRemoteControlBtn: false,
			LeaderCard:               "",
			CardType:                 "normalCard",
		}
		cardInfos = append(cardInfos, cardInfo)
	}

	// Get total cards
	counts, _ := e.repo.CountItems()

	c.JSON(http.StatusOK, CardSearchResponse{
		CardInfoSearch: struct {
			SearchID           string     `json:"searchID"`
			ResponseStatusStrg string     `json:"responseStatusStrg"`
			NumOfMatches       int        `json:"numOfMatches"`
			TotalMatches       int        `json:"totalMatches"`
			CardInfo           []CardInfo `json:"CardInfo"`
		}{
			SearchID:           "1",
			ResponseStatusStrg: "MORE",
			NumOfMatches:       len(cardInfos),
			TotalMatches:       counts.Cards,
			CardInfo:           cardInfos,
		},
	})
}

func (e *Emulator) handlePostCardRecord(c *gin.Context) {
	var payload struct {
		CardInfo struct {
			EmployeeNo string `json:"employeeNo"`
			CardNo     string `json:"cardNo"`
		} `json:"CardInfo"`
	}

	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	e.tracer.Info("[POST] /CardInfo/Record: content= %+v", payload.CardInfo)

	// Check whether it already exists
	exists, err := e.repo.CheckIfCardExists(payload.CardInfo.EmployeeNo, payload.CardInfo.CardNo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if exists {
		c.JSON(http.StatusBadRequest, gin.H{
			"statusCode":    6,
			"statusString":  "Invalid Content",
			"subStatusCode": "cardNoAlreadyExist",
			"errorCode":     1610637363,
			"errorMsg":      "checkEmployeeNo",
		})
		return
	}

	card := &Card{
		EmployeeNo: payload.CardInfo.EmployeeNo,
		CardNo:     payload.CardInfo.CardNo,
	}

	if err := e.repo.AddCard(card); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"statusCode":    1,
		"statusString":  "OK",
		"subStatusCode": "ok",
	})
}

// handlePutCardDelete handles PUT /ISAPI/AccessControl/CardInfo/Delete.
// Request body:
//
//	{"CardInfoDelCond": {"EmployeeNoList": [{"employeeNo": "1001"}]}}
//
// Clients send it as the first step of every card update (delete + Record);
// without it the following Record could hit cardNoAlreadyExist.
func (e *Emulator) handlePutCardDelete(c *gin.Context) {
	var payload struct {
		CardInfoDelCond struct {
			EmployeeNoList []struct {
				EmployeeNo string `json:"employeeNo"`
			} `json:"EmployeeNoList"`
		} `json:"CardInfoDelCond"`
	}

	if err := c.BindJSON(&payload); err != nil {
		e.tracer.Error("[CardInfo/Delete] invalid body: %v", err)
		writeHikvisionXML(c, http.StatusBadRequest, "6", "Error", "Invalid request")
		return
	}

	total := 0
	for _, item := range payload.CardInfoDelCond.EmployeeNoList {
		if item.EmployeeNo == "" {
			continue
		}
		n, err := e.deleteCardsFn(item.EmployeeNo)
		if err != nil {
			e.tracer.Error("[CardInfo/Delete] employeeNo=%s: %v", item.EmployeeNo, err)
			writeHikvisionXML(c, http.StatusInternalServerError, "6", "Error", err.Error())
			return
		}
		total += n
	}

	e.tracer.Info("[CardInfo/Delete] %d card(s) removed", total)
	writeHikvisionXML(c, http.StatusOK, "1", "OK", "ok")
}

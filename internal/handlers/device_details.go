package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/rphpires/facial-device-emulator/internal/database"

	"github.com/gin-gonic/gin"
)

const (
	deviceUsersDefaultPerPage = 10
	deviceUsersMaxPerPage     = 100
)

// getDeviceUsers lists the users stored on the device, in one format for
// both Hikvision and Dahua. It reads from the database, so it works with
// the emulator stopped.
func (h *Handler) getDeviceUsers(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	device, err := h.manager.GetDevice(id)
	if err != nil {
		h.writeError(c, err)
		return
	}

	page, perPage := parsePagination(c)
	search := strings.TrimSpace(c.Query("search"))

	users, total, err := database.ListDeviceUsers(
		context.Background(), h.manager.EmulatorDB, device.Model, id,
		search, perPage, (page-1)*perPage,
	)
	if err != nil {
		h.tracer.Error("Failed to list users of device %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"users":    users,
		"total":    total,
		"page":     page,
		"per_page": perPage,
		"model":    device.Model,
	})
}

// getDeviceSettings lists the settings stored for the device.
func (h *Handler) getDeviceSettings(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device ID"})
		return
	}

	if _, err := h.manager.GetDevice(id); err != nil {
		h.writeError(c, err)
		return
	}

	settings, err := database.ListDeviceSettings(context.Background(), h.manager.EmulatorDB, id)
	if err != nil {
		h.tracer.Error("Failed to list settings of device %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"settings": settings})
}

// parsePagination reads page/per_page from the query string with safe bounds.
func parsePagination(c *gin.Context) (page, perPage int) {
	page, err := strconv.Atoi(c.Query("page"))
	if err != nil || page < 1 {
		page = 1
	}

	perPage, err = strconv.Atoi(c.Query("per_page"))
	if err != nil || perPage < 1 {
		perPage = deviceUsersDefaultPerPage
	}
	if perPage > deviceUsersMaxPerPage {
		perPage = deviceUsersMaxPerPage
	}

	return page, perPage
}

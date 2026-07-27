package management

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/dataintegration"
	log "github.com/sirupsen/logrus"
)

// GetDataIntegrationStats returns statistics for the selected criteria.
func (h *Handler) GetDataIntegrationStats(c *gin.Context) {
	if h == nil || h.dataIntegrationStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "data integration storage is unavailable"})
		return
	}
	mask, errMask := dataintegration.MaskForKeys(c.QueryArray("criteria"))
	if errMask != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMask.Error()})
		return
	}
	timeRange, errRange := dataIntegrationTimeRange(c)
	if errRange != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errRange.Error()})
		return
	}
	stats, errStats := h.dataIntegrationStore.Stats(mask, timeRange)
	if errStats != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errStats.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// ClearDataIntegration removes all stored sessions and resets their statistics.
func (h *Handler) ClearDataIntegration(c *gin.Context) {
	if h == nil || h.dataIntegrationStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "data integration storage is unavailable"})
		return
	}
	var request struct {
		Confirm string `json:"confirm"`
	}
	if errBind := c.ShouldBindJSON(&request); errBind != nil || request.Confirm != "CLEAR_ALL_DATA" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirm must be CLEAR_ALL_DATA"})
		return
	}
	clearContext, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	result, errClear := h.dataIntegrationStore.Clear(clearContext)
	if errClear != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errClear.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// DownloadDataIntegrationZIP streams matching sessions to the administrator's browser.
func (h *Handler) DownloadDataIntegrationZIP(c *gin.Context) {
	if h == nil || h.dataIntegrationStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "data integration storage is unavailable"})
		return
	}
	mask, errMask := dataintegration.MaskForKeys(c.QueryArray("criteria"))
	if errMask != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMask.Error()})
		return
	}
	timeRange, errRange := dataIntegrationTimeRange(c)
	if errRange != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errRange.Error()})
		return
	}

	count, errCount := strconv.Atoi(strings.TrimSpace(c.Query("count")))
	if errCount != nil || count <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "count must be a positive integer"})
		return
	}
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "json")))
	if format != "json" && format != "jsonl" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "format must be json or jsonl"})
		return
	}
	layout := strings.ToLower(strings.TrimSpace(c.DefaultQuery("layout", dataintegration.ExportLayoutRaw)))
	if layout != dataintegration.ExportLayoutRaw && layout != dataintegration.ExportLayoutContract {
		c.JSON(http.StatusBadRequest, gin.H{"error": "layout must be raw or contract"})
		return
	}
	messageField := strings.ToLower(strings.TrimSpace(c.DefaultQuery("message_field", "messages")))
	switch messageField {
	case "messages", "conversation", "trajectory":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "message_field must be messages, conversation, or trajectory"})
		return
	}

	stats, errStats := h.dataIntegrationStore.Stats(mask, timeRange)
	if errStats != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errStats.Error()})
		return
	}
	if uint64(count) > stats.AvailableDownload {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("requested %d sessions but only %d match", count, stats.AvailableDownload),
		})
		return
	}

	fileName := fmt.Sprintf("data-integration-%s.zip", time.Now().UTC().Format("20060102-150405"))
	if layout == dataintegration.ExportLayoutContract {
		fileName = fmt.Sprintf("data-integration-contract-%s.zip", time.Now().UTC().Format("20060102-150405"))
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
	if errWrite := h.dataIntegrationStore.WriteZIPWithOptions(
		c.Writer,
		count,
		mask,
		timeRange,
		dataintegration.ExportOptions{
			Format:       format,
			Layout:       layout,
			MessageField: messageField,
		},
	); errWrite != nil {
		log.WithError(errWrite).Warn("failed to stream data integration zip")
	}
}

func dataIntegrationTimeRange(c *gin.Context) (dataintegration.TimeRange, error) {
	var timeRange dataintegration.TimeRange
	for key, target := range map[string]**time.Time{
		"from": &timeRange.From,
		"to":   &timeRange.To,
	} {
		value := strings.TrimSpace(c.Query(key))
		if value == "" {
			continue
		}
		parsed, errParse := time.Parse(time.RFC3339, value)
		if errParse != nil {
			return dataintegration.TimeRange{}, fmt.Errorf("%s must use RFC3339 format", key)
		}
		*target = &parsed
	}
	if timeRange.From != nil && timeRange.To != nil && timeRange.From.After(*timeRange.To) {
		return dataintegration.TimeRange{}, fmt.Errorf("from must not be later than to")
	}
	return timeRange, nil
}

package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		if errors.Is(errStats, dataintegration.ErrInitializing) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "data integration statistics are initializing"})
			return
		}
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

// ExportDataIntegrationToolSchemas returns the complete local tool registry.
func (h *Handler) ExportDataIntegrationToolSchemas(c *gin.Context) {
	if h == nil || h.dataIntegrationStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "data integration storage is unavailable"})
		return
	}
	data, errExport := h.dataIntegrationStore.ExportToolSchemas()
	if errExport != nil {
		dataIntegrationSchemaError(c, errExport)
		return
	}
	if c.Query("download") == "1" {
		fileName := fmt.Sprintf("tool-schema-registry-%s.json", time.Now().UTC().Format("20060102-150405"))
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

// ImportDataIntegrationToolSchemas non-destructively merges every valid signature.
func (h *Handler) ImportDataIntegrationToolSchemas(c *gin.Context) {
	if h == nil || h.dataIntegrationStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "data integration storage is unavailable"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<20)
	payload, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tool schema registry must be valid JSON and no larger than 128 MiB"})
		return
	}
	result, errImport := h.dataIntegrationStore.ImportToolSchemas(payload)
	if errImport != nil {
		dataIntegrationSchemaError(c, errImport)
		return
	}
	c.JSON(http.StatusOK, result)
}

// PutDataIntegrationToolSchema saves one complete definition as a new version.
func (h *Handler) PutDataIntegrationToolSchema(c *gin.Context) {
	if h == nil || h.dataIntegrationStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "data integration storage is unavailable"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	var request struct {
		Definition json.RawMessage `json:"definition"`
	}
	if errBind := c.ShouldBindJSON(&request); errBind != nil || len(request.Definition) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "definition must be a JSON object and no larger than 2 MiB"})
		return
	}
	result, errPut := h.dataIntegrationStore.PutToolSchema(c.Param("name"), request.Definition)
	if errPut != nil {
		dataIntegrationSchemaError(c, errPut)
		return
	}
	c.JSON(http.StatusOK, result)
}

// BackfillDataIntegrationToolSchemas persists compatible missing definitions
// into sessions matching the current filters and refreshes their statistics.
func (h *Handler) BackfillDataIntegrationToolSchemas(c *gin.Context) {
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
	result, errBackfill := h.dataIntegrationStore.BackfillToolSchemas(c.Request.Context(), mask, timeRange)
	if errBackfill != nil {
		dataIntegrationSchemaError(c, errBackfill)
		return
	}
	c.JSON(http.StatusOK, result)
}

func dataIntegrationSchemaError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, dataintegration.ErrInvalidToolSchema):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, dataintegration.ErrInitializing):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "data integration storage is initializing"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
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

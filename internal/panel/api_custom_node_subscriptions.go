package panel

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
)

type customNodeSubscriptionReq struct {
	Name                  string `json:"name"`
	URL                   string `json:"url"`
	Group                 string `json:"group"`
	Enabled               *bool  `json:"enabled"`
	AutoUpdate            *bool  `json:"auto_update"`
	UpdateIntervalMinutes int    `json:"update_interval_minutes"`
	BaseSortOrder         int    `json:"base_sort_order"`
}

func (a *App) listCustomNodeSubscriptions(c *gin.Context) {
	var rows []model.CustomNodeSubscription
	if err := a.db.Order("id").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"subscriptions": rows})
}

func (a *App) createCustomNodeSubscription(c *gin.Context) {
	var req customNodeSubscriptionReq
	if !bindJSON(c, &req) {
		return
	}
	url, err := validateCustomNodeSubscriptionURL(req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := trimRunes(strings.TrimSpace(req.Name), 128)
	if name == "" {
		name = "订阅"
	}
	autoUpdate := true
	if req.AutoUpdate != nil {
		autoUpdate = *req.AutoUpdate
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	row := model.CustomNodeSubscription{
		Name: name, URL: url, Group: trimRunes(strings.TrimSpace(req.Group), 64),
		Enabled: enabled, AutoUpdate: autoUpdate, UpdateIntervalMinutes: normalizeCustomNodeSubscriptionInterval(req.UpdateIntervalMinutes),
		BaseSortOrder: req.BaseSortOrder,
	}
	if err := a.db.Create(&row).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	result, err := a.subscriptionManager().sync(c.Request.Context(), row.ID)
	if err != nil {
		a.db.First(&row, row.ID)
		c.JSON(http.StatusOK, gin.H{"subscription": row, "sync": result, "sync_error": err.Error()})
		return
	}
	a.db.First(&row, row.ID)
	c.JSON(http.StatusOK, gin.H{"subscription": row, "sync": result})
}

func (a *App) updateCustomNodeSubscription(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	var req customNodeSubscriptionReq
	if !bindJSON(c, &req) {
		return
	}
	url, err := validateCustomNodeSubscriptionURL(req.URL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	unlock := a.lockCustomNodeSubscription(&id)
	defer unlock()
	var row model.CustomNodeSubscription
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&row, id).Error; err != nil {
			return err
		}
		oldBaseSortOrder := row.BaseSortOrder
		autoUpdate := row.AutoUpdate
		if req.AutoUpdate != nil {
			autoUpdate = *req.AutoUpdate
		}
		enabled := row.Enabled
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		name := trimRunes(strings.TrimSpace(req.Name), 128)
		if name == "" {
			name = "订阅"
		}
		group := trimRunes(strings.TrimSpace(req.Group), 64)
		baseSortOrder := req.BaseSortOrder
		updates := map[string]any{
			"name": name, "url": url, "group": group, "enabled": enabled, "auto_update": autoUpdate,
			"update_interval_minutes": normalizeCustomNodeSubscriptionInterval(req.UpdateIntervalMinutes),
			"base_sort_order":         baseSortOrder,
		}
		if err := tx.Model(&row).Updates(updates).Error; err != nil {
			return err
		}
		// Apply source-level presentation changes immediately, including a base
		// sort offset, without waiting for a remote fetch.
		if err := tx.Model(&model.CustomNode{}).Where("subscription_id = ?", id).Updates(map[string]any{
			"group": group, "enabled": enabled,
		}).Error; err != nil {
			return err
		}
		delta := baseSortOrder - oldBaseSortOrder
		if delta != 0 {
			if err := tx.Model(&model.CustomNode{}).Where("subscription_id = ?", id).
				UpdateColumn("sort_order", gorm.Expr("sort_order + ?", delta)).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.db.First(&row, id)
	c.JSON(http.StatusOK, gin.H{"subscription": row})
}

func (a *App) deleteCustomNodeSubscription(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	unlock := a.lockCustomNodeSubscription(&id)
	defer unlock()
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var row model.CustomNodeSubscription
		if err := tx.First(&row, id).Error; err != nil {
			return err
		}
		if err := tx.Where("subscription_id = ?", id).Delete(&model.CustomNode{}).Error; err != nil {
			return err
		}
		return tx.Delete(&row).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (a *App) syncCustomNodeSubscription(c *gin.Context) {
	id, ok := uintParam(c, "id")
	if !ok {
		return
	}
	result, err := a.subscriptionManager().sync(c.Request.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "subscription not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var row model.CustomNodeSubscription
	a.db.First(&row, id)
	c.JSON(http.StatusOK, gin.H{"subscription": row, "sync": result})
}

package server

import (
	"net/http"

	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

// Read errors must not become successful DTOs containing default user data or
// empty actors. Callers must return before serializing or caching on !ok.
func (a *App) itemDataFor(c *gin.Context, ids []int64) (map[int64]store.UserData, bool) {
	data, err := a.db.DataForUser(a.currentUserID(c), ids)
	if err != nil {
		itemDataUnavailable(c)
		return nil, false
	}
	return data, true
}

func (a *App) itemAssociations(c *gin.Context, ids []int64) (map[int64]store.UserData, map[int64][]store.ActorRef, bool) {
	data, ok := a.itemDataFor(c, ids)
	if !ok {
		return nil, nil, false
	}
	actors, err := a.db.ActorsFor(ids)
	if err != nil {
		itemDataUnavailable(c)
		return nil, nil, false
	}
	return data, actors, true
}

func itemDataUnavailable(c *gin.Context) {
	c.JSON(http.StatusInternalServerError, gin.H{"error": "item data unavailable", "code": "ITEM_DATA_UNAVAILABLE"})
}

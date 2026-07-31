package panel

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/singpanel/singpanel/internal/model"
)

const sessionTTL = 72 * time.Hour

type loginReq struct {
	Username string `json:"username"`
	// email is accepted for backward compatibility; the account identifier is a
	// username stored in the User.Email column.
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin authenticates by username + password. Self-registration is
// disabled: accounts are created by an admin in the panel.
// tooMany rejects a caller that is currently throttled.
func tooMany(c *gin.Context, wait time.Duration) {
	secs := int(wait.Seconds()) + 1
	c.Header("Retry-After", strconv.Itoa(secs))
	c.JSON(http.StatusTooManyRequests, gin.H{
		"error": fmt.Sprintf("尝试过于频繁，请 %d 秒后再试", secs),
	})
}

// loginFailed records the failure and answers 401, or 429 once the account has
// been guessed at too often.
func (a *App) loginFailed(c *gin.Context, ipKey, pairKey, acctKey string) {
	a.login.fail(ipKey, pairKey, acctKey)
	if wait := a.login.retryAfter(acctKey, pairKey); wait > 0 {
		tooMany(c, wait)
		return
	}
	c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
}

func (a *App) handleLogin(c *gin.Context) {
	var req loginReq
	if !bindJSON(c, &req) {
		return
	}
	account := req.Username
	if account == "" {
		account = req.Email
	}
	account = strings.ToLower(strings.TrimSpace(account))

	// Nothing is rejected before the password is checked except a source address
	// that is clearly hammering the endpoint — so whoever knows the password can
	// always get in, and neither a stranger typing the owner's username nor the
	// owner's own typos can lock the panel.
	ip := clientIP(c)
	ipKey := "ip:" + ip
	pairKey := "ip:" + ip + "|acct:" + account
	acctKey := "acct:" + account
	if wait := a.login.retryAfterHard(ipKey, ipHardFails); wait > 0 {
		tooMany(c, wait)
		return
	}

	// Case-insensitive username match (the stored name preserves its case).
	var u model.User
	if err := a.db.Where("LOWER(email) = ?", account).First(&u).Error; err != nil {
		// Spend the same time as a real bcrypt compare so a missing account is
		// not distinguishable from a wrong password by response timing.
		checkPassword(dummyHash, req.Password)
		a.loginFailed(c, ipKey, pairKey, acctKey)
		return
	}
	if !checkPassword(u.Password, req.Password) {
		a.loginFailed(c, ipKey, pairKey, acctKey)
		return
	}
	// Correct password: always accepted, even while the account key is throttled.
	a.login.succeed(ipKey, pairKey, acctKey)
	// Disabled or expired accounts must not get a session either.
	if u.Role != model.RoleAdmin && !userActive(&u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "账号已停用或已到期"})
		return
	}
	tok, err := a.auth.Issue(&u, sessionTTL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tok, "user": u})
}

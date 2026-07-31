package panel

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/model"
)

// Claims is the JWT payload for panel sessions.
type Claims struct {
	UID  uint       `json:"uid"`
	Role model.Role `json:"role"`
	Ver  uint       `json:"ver"`
	jwt.RegisteredClaims
}

// Auth issues and validates session tokens.
type Auth struct {
	secret []byte
	db     *gorm.DB
}

// minSecretLen is the shortest JWT secret accepted. Anything shorter is
// brute-forceable offline from a single captured token.
const minSecretLen = 24

// NewAuth builds an Auth.
//
// An empty secret generates a random ephemeral one (sessions simply do not
// survive a restart). A secret that is set but guessable is far worse than
// none: the signing key is what separates a stranger from a forged
// {"role":"admin"} token, and placeholders like "change-me" are published in
// this repo. Refuse to start rather than serve with one.
func NewAuth(secret string, db *gorm.DB) *Auth {
	if secret == "" {
		return &Auth{secret: []byte(randHex(32)), db: db}
	}
	if isWeakSecret(secret) {
		log.Fatalf("JWT_SECRET is unsafe (placeholder or shorter than %d chars). "+
			"Generate one with:  openssl rand -hex 32", minSecretLen)
	}
	return &Auth{secret: []byte(secret), db: db}
}

// isWeakSecret rejects the placeholders shipped in this repo's example configs
// and anything too short to resist an offline attack.
func isWeakSecret(secret string) bool {
	if len(secret) < minSecretLen {
		return true
	}
	l := strings.ToLower(secret)
	for _, bad := range []string{"change-me", "changeme", "your-secret", "secret", "password"} {
		if strings.Contains(l, bad) {
			return true
		}
	}
	return false
}

// Issue mints a signed token for a user.
func (a *Auth) Issue(u *model.User, ttl time.Duration) (string, error) {
	claims := Claims{
		UID:  u.ID,
		Role: u.Role,
		Ver:  u.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.secret)
}

func (a *Auth) parse(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return a.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// Middleware authenticates a request and stores uid/role in the context.
func (a *Auth) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := bearerToken(c)
		if tok == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		claims, err := a.parse(tok)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		var u model.User
		if a.db == nil || a.db.First(&u, claims.UID).Error != nil || !u.Enabled ||
			u.Role != claims.Role || u.TokenVersion != claims.Ver ||
			(u.Role != model.RoleAdmin && u.Expired(time.Now())) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session revoked"})
			return
		}
		c.Set("uid", claims.UID)
		c.Set("role", u.Role)
		c.Next()
	}
}

// AdminOnly rejects non-admin sessions.
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if role, _ := c.Get("role"); role != model.RoleAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.Next()
	}
}

func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	// Fallback: allow ?token= for convenience (e.g. EventSource).
	return c.Query("token")
}

func currentUID(c *gin.Context) uint {
	if v, ok := c.Get("uid"); ok {
		if id, ok := v.(uint); ok {
			return id
		}
	}
	return 0
}

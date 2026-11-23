package panel

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

const (
	jwtIssuer   = "singbox-panel"
	jwtAudience = "singbox-panel-web"
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
	distinct := make(map[rune]struct{}, len(secret))
	for _, r := range secret {
		distinct[r] = struct{}{}
	}
	return len(distinct) < 8
}

// Issue mints a signed token for a user.
func (a *Auth) Issue(u *model.User, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UID:  u.ID,
		Role: u.Role,
		Ver:  u.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			Issuer:    jwtIssuer,
			Audience:  jwt.ClaimStrings{jwtAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
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
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(jwtIssuer), jwt.WithAudience(jwtAudience))
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
		if a.db == nil || a.db.First(&u, claims.UID).Error != nil || !userActive(&u) ||
			u.Role != claims.Role || u.TokenVersion != claims.Ver {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session revoked"})
			return
		}
		c.Set("uid", claims.UID)
		c.Set("role", u.Role)
		c.Set("username", u.Email)
		c.Set("session_claims", claims)
		c.Next()
	}
}

// sessionActive rechecks a long-lived request against the current account.
// A valid signature alone does not survive logout, password rotation or expiry.
func (a *Auth) sessionActive(ctx context.Context, claims *Claims) bool {
	if a == nil || a.db == nil || claims == nil || claims.ExpiresAt == nil || !time.Now().Before(claims.ExpiresAt.Time) {
		return false
	}
	var user model.User
	return a.db.WithContext(ctx).Select("id", "role", "token_version", "enabled", "expire_at").First(&user, claims.UID).Error == nil &&
		userActive(&user) && user.Role == claims.Role && user.TokenVersion == claims.Ver
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
	return ""
}

func currentUID(c *gin.Context) uint {
	if v, ok := c.Get("uid"); ok {
		if id, ok := v.(uint); ok {
			return id
		}
	}
	return 0
}

// redis.go — Memorystore Redis 7.2 implementations of the realtime ports
// (ADR-168). Thin wrappers over go-redis/v9: pub/sub for the Backplane,
// HINCRBY/HGETALL for the TallyStore, ZINCRBY/ZREVRANGE for the
// LeaderboardStore. These are the production hot-path adapters; they are
// exercised in the scale-smoke (miniredis is unavailable offline, so they
// carry no unit tests — the logic-bearing code lives in mem.go + the domain
// TimeDecayScore, which are unit-tested).
//
// Connection config comes from env (Secret Manager-sourced) per
// feedback_no_inline_config — never hard-coded:
//
//	CHORA_REDIS_ADDR     host:port
//	CHORA_REDIS_PASSWORD AUTH string ("" → no auth)
//	CHORA_REDIS_CA_CERT  PEM of the instance server CA ("" → plaintext, no TLS)
//
// Memorystore with transit_encryption_mode=SERVER_AUTHENTICATION presents a
// cert signed by the per-instance CA. Because we dial the instance's PRIVATE IP
// (which the cert is NOT issued for), standard hostname verification fails — so
// we verify the cert CHAIN against the instance CA ourselves and skip only the
// hostname check. This is MITM-resistant (chain-verified) while tolerating the
// IP/CN mismatch inherent to private-IP Memorystore access.
package realtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Redis implements Backplane + TallyStore + LeaderboardStore over one client.
type Redis struct {
	c *redis.Client
}

// RedisConfig is the connection config for the Memorystore adapter. All fields
// are env/secret-sourced at the wiring layer (no inline config).
type RedisConfig struct {
	Addr      string // host:port (CHORA_REDIS_ADDR)
	Password  string // AUTH string (CHORA_REDIS_PASSWORD); "" → no auth
	CACertPEM string // server-CA PEM (CHORA_REDIS_CA_CERT); "" → plaintext
}

// buildRedisTLS builds the *tls.Config for a Memorystore SERVER_AUTHENTICATION
// endpoint from the instance server-CA PEM. Returns (nil, nil) when caPEM is
// empty (plaintext). Fails loud on un-parseable PEM.
func buildRedisTLS(caPEM string) (*tls.Config, error) {
	if strings.TrimSpace(caPEM) == "" {
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, errors.New("realtime: CHORA_REDIS_CA_CERT is not a valid PEM certificate")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		// Hostname check skipped (we dial the private IP, not the cert CN); the
		// chain is verified against the instance CA below.
		InsecureSkipVerify: true, //nolint:gosec // chain verified in VerifyPeerCertificate
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			certs := make([]*x509.Certificate, 0, len(rawCerts))
			for _, raw := range rawCerts {
				c, err := x509.ParseCertificate(raw)
				if err != nil {
					return err
				}
				certs = append(certs, c)
			}
			if len(certs) == 0 {
				return errors.New("realtime: redis server presented no certificate")
			}
			inter := x509.NewCertPool()
			for _, c := range certs[1:] {
				inter.AddCert(c)
			}
			_, err := certs[0].Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter})
			return err
		},
	}, nil
}

// NewRedis dials cfg.Addr with optional AUTH + TLS (SERVER_AUTHENTICATION).
// The caller owns Close.
func NewRedis(cfg RedisConfig) (*Redis, error) {
	tlsCfg, err := buildRedisTLS(cfg.CACertPEM)
	if err != nil {
		return nil, err
	}
	return &Redis{c: redis.NewClient(&redis.Options{
		Addr:      cfg.Addr,
		Password:  cfg.Password,
		TLSConfig: tlsCfg,
	})}, nil
}

// Ping verifies connectivity (used at boot to fail loud on a bad endpoint).
func (r *Redis) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }

// Close releases the underlying client.
func (r *Redis) Close() error { return r.c.Close() }

// --- Backplane ---------------------------------------------------------------

// Publish broadcasts payload on channel (Redis PUBLISH).
func (r *Redis) Publish(ctx context.Context, channel string, payload []byte) error {
	return r.c.Publish(ctx, channel, payload).Err()
}

// PSubscribe pattern-subscribes (Redis PSUBSCRIBE) and adapts go-redis messages
// to BackplaneMessage. cancel closes the subscription.
func (r *Redis) PSubscribe(ctx context.Context, pattern string) (<-chan BackplaneMessage, func() error, error) {
	ps := r.c.PSubscribe(ctx, pattern)
	out := make(chan BackplaneMessage, 256)
	go func() {
		defer close(out)
		for msg := range ps.Channel() {
			select {
			case out <- BackplaneMessage{Channel: msg.Channel, Payload: []byte(msg.Payload)}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, ps.Close, nil
}

// --- TallyStore --------------------------------------------------------------

func tallyKey(sessionID, questionID string) string {
	return channelPrefix + "tally:" + sessionID + ":" + questionID
}

// Incr bumps the per-choice count (Redis HINCRBY).
func (r *Redis) Incr(ctx context.Context, sessionID, questionID, choice string) (int64, error) {
	return r.c.HIncrBy(ctx, tallyKey(sessionID, questionID), choice, 1).Result()
}

// Snapshot returns choice→count (Redis HGETALL).
func (r *Redis) Snapshot(ctx context.Context, sessionID, questionID string) (map[string]int64, error) {
	raw, err := r.c.HGetAll(ctx, tallyKey(sessionID, questionID)).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(raw))
	for choice, v := range raw {
		n, _ := strconv.ParseInt(v, 10, 64)
		out[choice] = n
	}
	return out, nil
}

// --- LeaderboardStore --------------------------------------------------------

func lbKey(sessionID string) string { return channelPrefix + "lb:" + sessionID }

// Credit adds points to a participant's cumulative score (Redis ZINCRBY).
func (r *Redis) Credit(ctx context.Context, sessionID, gcid string, points int64) (int64, error) {
	total, err := r.c.ZIncrBy(ctx, lbKey(sessionID), float64(points), gcid).Result()
	if err != nil {
		return 0, err
	}
	return int64(total), nil
}

// Top returns the n highest scorers, rank 1 first (Redis ZREVRANGE WITHSCORES).
func (r *Redis) Top(ctx context.Context, sessionID string, n int) ([]LeaderboardEntry, error) {
	stop := int64(n - 1)
	if n <= 0 {
		stop = -1
	}
	zs, err := r.c.ZRevRangeWithScores(ctx, lbKey(sessionID), 0, stop).Result()
	if err != nil {
		return nil, err
	}
	out := make([]LeaderboardEntry, 0, len(zs))
	for i, z := range zs {
		gcid, _ := z.Member.(string)
		out = append(out, LeaderboardEntry{GCID: gcid, Score: int64(z.Score), Rank: i + 1})
	}
	return out, nil
}

// Compile-time assertions: Redis satisfies all three realtime ports.
var (
	_ Backplane        = (*Redis)(nil)
	_ TallyStore       = (*Redis)(nil)
	_ LeaderboardStore = (*Redis)(nil)
)

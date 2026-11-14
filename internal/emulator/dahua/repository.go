package dahua

import (
	"context"
	"errors"
	"fmt"
	"github.com/rphpires/facial-device-emulator/internal/cache"
	"github.com/rphpires/facial-device-emulator/internal/database"
	"github.com/rphpires/facial-device-emulator/internal/models"
	"time"
)

// ErrCardUserIDTaken and ErrCardNoTaken distinguish the two reasons the real
// device (measured on the bench) rejects a recordUpdater.cgi?action=insert on
// AccessControlCard. The device allows only ONE row per UserID
// (ErrCardUserIDTaken, code 286064929) and does not allow the same CardNo on
// rows of two different UserIDs (ErrCardNoTaken, code 286064930).
var (
	ErrCardUserIDTaken = errors.New("dahua: UserID already has an AccessControlCard card")
	ErrCardNoTaken     = errors.New("dahua: CardNo belongs to a different UserID")
)

// Predefined queries (automatically prepared statements)
const (
	queryCardCount = "SELECT COUNT(*) FROM emulator.dahua_cards WHERE device_id = $1"
	queryFaceCount = "SELECT COUNT(*) FROM emulator.dahua_faces WHERE device_id = $1"

	// Single query that fetches every count at once
	queryAllCounts = `
        SELECT 
            (SELECT COUNT(*) FROM emulator.dahua_cards WHERE device_id = $1) as cards,
            (SELECT COUNT(*) FROM emulator.dahua_faces WHERE device_id = $1) as faces
    `

	queryRandomCard = `
        SELECT card_name, card_no, user_id 
        FROM emulator.dahua_cards 
        WHERE device_id = $1
        ORDER BY RANDOM() LIMIT 1
    `

	queryNextRecNo = `
        SELECT COALESCE(MIN(t1.rec_no + 1), 1) 
        FROM emulator.dahua_cards AS t1
        LEFT JOIN emulator.dahua_cards AS t2 ON t1.rec_no + 1 = t2.rec_no AND t1.device_id = t2.device_id
        WHERE t1.device_id = $1 AND t2.rec_no IS NULL
    `
)

// Repository handles the Dahua-specific database operations.
type Repository struct {
	db       database.DBInterface // AdaptivePool or DualPoolManager
	deviceID int
	cache    *cache.SimpleCache
	timeout  time.Duration
}

// NewRepository creates a new Dahua repository.
func NewRepository(db database.DBInterface, deviceID int) *Repository {
	return &Repository{
		db:       db,
		deviceID: deviceID,
		cache:    cache.NewSimpleCache(),
		timeout:  10 * time.Second, // Longer timeout for high concurrency
	}
}

// getWriteContext creates a context for write operations carrying the EmulatorID.
func (r *Repository) getWriteContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	ctx = database.WithEmulatorID(ctx, r.deviceID)
	return ctx, cancel
}

// ====================== SETTINGS ======================

// GetSetting returns a device setting.
func (r *Repository) GetSetting(key string) (string, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	var value string
	err := r.db.QueryRow(ctx,
		"SELECT value FROM emulator.device_settings WHERE device_id = $1 AND cfg_id = $2",
		r.deviceID, key).Scan(&value)
	if err != nil {
		// Fall back to defaults for some settings
		switch key {
		case "RemoteServer":
			return "localhost", nil
		case "RemotePort":
			return models.DefaultEventServerPort, nil
		case "LocalAuthentication":
			return "1", nil
		default:
			return "", err
		}
	}
	return value, err
}

// SetSetting stores a device setting.
func (r *Repository) SetSetting(key, value string) error {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	_, err := r.db.Exec(ctx,
		`INSERT INTO emulator.device_settings (device_id, cfg_id, value) 
         VALUES ($1, $2, $3) 
         ON CONFLICT (device_id, cfg_id) DO UPDATE SET value = $3, updated_at = NOW()`,
		r.deviceID, key, value)
	return err
}

// ====================== COUNT OPERATIONS ======================

// GetTotalUsers returns the cached user count (Dahua counts cards as users).
func (r *Repository) GetTotalUsers() (int, error) {
	// Try the cache first
	if count, found := r.cache.GetUserCount(r.deviceID); found {
		return count, nil
	}

	// Cache miss - query the database
	ctx, cancel := r.getWriteContext()
	defer cancel()

	var count int
	err := r.db.QueryRow(ctx, queryCardCount, r.deviceID).Scan(&count)
	if err != nil {
		return 0, err
	}

	// Cache for 60 seconds
	r.cache.SetUserCount(r.deviceID, count, 60*time.Second)
	return count, nil
}

// CountItems returns every count in a single query.
func (r *Repository) CountItems() (*CountItems, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	counts := &CountItems{}
	err := r.db.QueryRow(ctx, queryAllCounts, r.deviceID).Scan(
		&counts.Cards, &counts.Faces)

	if err != nil {
		return nil, fmt.Errorf("failed to count items: %w", err)
	}

	// Cache the card (user) count
	r.cache.SetUserCount(r.deviceID, counts.Cards, 60*time.Second)

	return counts, nil
}

// ====================== CARD OPERATIONS ======================

// cardExistsForUserID reports whether the UserID already has a row in
// AccessControlCard. The real device allows only ONE per UserID.
func (r *Repository) cardExistsForUserID(userID int) (bool, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	var count int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM emulator.dahua_cards
         WHERE device_id = $1 AND user_id = $2`,
		r.deviceID, userID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// cardNoBelongsToOtherUser reports whether the CardNo is already bound to a
// UserID other than the one being inserted.
func (r *Repository) cardNoBelongsToOtherUser(cardNo string, userID int) (bool, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	var count int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM emulator.dahua_cards
         WHERE device_id = $1 AND card_no = $2 AND user_id <> $3`,
		r.deviceID, cardNo, userID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetNextRecNo returns the next free RecNo.
func (r *Repository) GetNextRecNo() (int, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	var recNo int
	err := r.db.QueryRow(ctx, queryNextRecNo, r.deviceID).Scan(&recNo)

	if err != nil {
		// No records: start at 1
		recNo = 1
	}

	return recNo, nil
}

// AddCard adds a new card.
func (r *Repository) AddCard(cardName string, userID int, cardNo string, validDateStart, validDateEnd time.Time) (int, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	// Order measured on the bench: a UserID that already has a card is
	// rejected (286064929) even if the CardNo is new; only after that is a
	// CardNo duplicated from another UserID rejected (286064930). The two cases
	// were never measured combined, but this order is the only testable one
	// given the two errors were mutually exclusive in the bench samples.
	hasCard, err := r.cardExistsForUserID(userID)
	if err != nil {
		return 0, err
	}
	if hasCard {
		return 0, ErrCardUserIDTaken
	}

	ownedByOther, err := r.cardNoBelongsToOtherUser(cardNo, userID)
	if err != nil {
		return 0, err
	}
	if ownedByOther {
		return 0, ErrCardNoTaken
	}

	// Get the next RecNo
	recNo, err := r.GetNextRecNo()
	if err != nil {
		return 0, err
	}

	_, err = r.db.Exec(ctx,
		`INSERT INTO emulator.dahua_cards (device_id, rec_no, card_name, user_id, card_no, valid_date_start, valid_date_end) 
         VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		r.deviceID, recNo, cardName, userID, cardNo, validDateStart, validDateEnd)

	// Invalidate the cache ONLY on success
	if err == nil {
		r.cache.InvalidateDevice(r.deviceID)
	}

	return recNo, err
}

// RemoveCard removes a card by RecNo.
func (r *Repository) RemoveCard(recNo int) error {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	_, err := r.db.Exec(ctx,
		"DELETE FROM emulator.dahua_cards WHERE device_id = $1 AND rec_no = $2",
		r.deviceID, recNo)

	// Invalidate the cache ONLY on success
	if err == nil {
		r.cache.InvalidateDevice(r.deviceID)
	}

	return err
}

// FindCard finds cards by UserID, returned in Dahua format.
func (r *Repository) FindCard(userID int) (string, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	rows, err := r.db.Query(ctx,
		`SELECT rec_no, card_name, user_id, card_no, valid_date_start, valid_date_end 
         FROM emulator.dahua_cards 
         WHERE device_id = $1 AND user_id = $2`,
		r.deviceID, userID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var cards []*Card
	for rows.Next() {
		card := &Card{}
		err := rows.Scan(&card.RecNo, &card.CardName, &card.UserID, &card.CardNo,
			&card.ValidDateStart, &card.ValidDateEnd)
		if err != nil {
			return "", err
		}
		cards = append(cards, card)
	}

	if len(cards) == 0 {
		return "found=0", nil
	}

	// Format the response Dahua-style
	response := fmt.Sprintf("found=1\n%s", r.formatCardToResponse(cards))
	return response, nil
}

// GetCards returns a page of cards in Dahua format.
func (r *Repository) GetCards(count, offset int) (string, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	rows, err := r.db.Query(ctx,
		`SELECT rec_no, card_name, user_id, card_no, valid_date_start, valid_date_end 
         FROM emulator.dahua_cards 
         WHERE device_id = $1
         ORDER BY rec_no
         LIMIT $2 OFFSET $3`,
		r.deviceID, count, offset)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var cards []*Card
	for rows.Next() {
		card := &Card{}
		err := rows.Scan(&card.RecNo, &card.CardName, &card.UserID, &card.CardNo,
			&card.ValidDateStart, &card.ValidDateEnd)
		if err != nil {
			return "", err
		}
		cards = append(cards, card)
	}

	if len(cards) == 0 {
		return "found=0", nil
	}

	// Format the response Dahua-style
	response := fmt.Sprintf("found=%d\n%s", len(cards), r.formatCardToResponse(cards))
	return response, nil
}

// formatCardToResponse renders cards as Dahua records[i].* lines.
func (r *Repository) formatCardToResponse(cards []*Card) string {
	response := ""
	for i, card := range cards {
		rec := fmt.Sprintf(`records[%d].CardName=%s
records[%d].CardNo=%s
records[%d].CardStatus=0
records[%d].CardType=0
records[%d].CitizenIDNo=
records[%d].Doors[0]=0
records[%d].DynamicCheckCode=
records[%d].FirstEnter=false
records[%d].Handicap=false
records[%d].IsValid=false
records[%d].Password=
records[%d].RecNo=%d
records[%d].RepeatEnterRouteTimeout=4294967295
records[%d].TimeSections[0]=1
records[%d].UseTime=200
records[%d].UserID=%d
records[%d].UserType=0
records[%d].VTOPosition=
records[%d].ValidDateEnd=%s
records[%d].ValidDateStart=%s
`,
			i, card.CardName,
			i, card.CardNo,
			i,
			i,
			i,
			i,
			i,
			i,
			i,
			i,
			i,
			i, card.RecNo,
			i,
			i,
			i,
			i, card.UserID,
			i,
			i,
			i, card.ValidDateEnd.Format("2006-01-02 15:04:05"),
			i, card.ValidDateStart.Format("2006-01-02 15:04:05"))
		response += rec
	}
	return response
}

// ====================== FACE OPERATIONS ======================

// CheckIfFaceExists reports whether the user already has a face.
func (r *Repository) CheckIfFaceExists(userID int) (bool, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	var count int
	err := r.db.QueryRow(ctx,
		"SELECT COUNT(*) FROM emulator.dahua_faces WHERE device_id = $1 AND user_id = $2",
		r.deviceID, userID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// AddFace adds (or replaces) a face.
func (r *Repository) AddFace(userID int, md5 string) error {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	_, err := r.db.Exec(ctx,
		`INSERT INTO emulator.dahua_faces (device_id, user_id, md5_hash) 
         VALUES ($1, $2, $3)
         ON CONFLICT (device_id, user_id) DO UPDATE SET md5_hash = $3, updated_at = NOW()`,
		r.deviceID, userID, md5)

	// Invalidate the cache ONLY on success
	if err == nil {
		r.cache.InvalidateDevice(r.deviceID)
	}

	return err
}

// RemoveFace removes a face.
func (r *Repository) RemoveFace(userID int) error {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	_, err := r.db.Exec(ctx,
		"DELETE FROM emulator.dahua_faces WHERE device_id = $1 AND user_id = $2",
		r.deviceID, userID)

	// Invalidate the cache ONLY on success
	if err == nil {
		r.cache.InvalidateDevice(r.deviceID)
	}

	return err
}

// FindRemoteFaces returns the face search info - Total is the device's
// GLOBAL count (no UserID filter). It is only correct when the request has no
// Condition.UserID; see FindRemoteFacesByUserID for the filtered case.
func (r *Repository) FindRemoteFaces() (*FindFaceResponse, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	var count int
	err := r.db.QueryRow(ctx, queryFaceCount, r.deviceID).Scan(&count)
	if err != nil {
		return nil, err
	}

	// Pseudo-random token between 1 and 30
	return &FindFaceResponse{
		Token: 1 + (int(time.Now().Unix()) % 30),
		Total: count,
	}, nil
}

// FindRemoteFacesByUserID resolves Total for ONE specific UserID - 0 or 1,
// never the global count. Measured on the bench: FaceInfoManager.cgi?action=
// startFind&Condition.UserID=<id> returns Total=0 when the UserID has no face
// and Total=1 when it has one (the vendor spec, section 12.3.4, documents
// exactly that: "Total ... return 0 if not found"). Clients use this Total
// to decide HasFace; the global count would never yield false.
func (r *Repository) FindRemoteFacesByUserID(userID int) (*FindFaceResponse, error) {
	exists, err := r.CheckIfFaceExists(userID)
	if err != nil {
		return nil, err
	}

	total := 0
	if exists {
		total = 1
	}

	// Token carries no measured meaning (the spec documents no expected
	// value) - kept on the same scheme as FindRemoteFaces so the endpoint
	// does not get a second Token convention.
	return &FindFaceResponse{
		Token: 1 + (int(time.Now().Unix()) % 30),
		Total: total,
	}, nil
}

// GetRemoteFaces returns a page of faces.
func (r *Repository) GetRemoteFaces(count, offset int) (*GetFaceResponse, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	rows, err := r.db.Query(ctx,
		`SELECT user_id, md5_hash 
         FROM emulator.dahua_faces 
         WHERE device_id = $1
         ORDER BY user_id
         LIMIT $2 OFFSET $3`,
		r.deviceID, count, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var faces []FaceInfo
	for rows.Next() {
		var userID int
		var md5 string
		err := rows.Scan(&userID, &md5)
		if err != nil {
			return nil, err
		}
		faces = append(faces, FaceInfo{
			MD5:    md5,
			UserID: userID,
		})
	}

	return &GetFaceResponse{Info: faces}, nil
}

// ====================== UTILITY OPERATIONS ======================

// GetRandomCard returns a random card for event generation.
func (r *Repository) GetRandomCard() (cardName, cardNo string, userID int, err error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	err = r.db.QueryRow(ctx, queryRandomCard, r.deviceID).Scan(&cardName, &cardNo, &userID)
	return
}

// GetCardUserIDByRecNo resolves the UserID from a RecNo. Clients remove
// cards by RecNo, but faces are keyed by UserID — and the real device drops
// both together.
func (r *Repository) GetCardUserIDByRecNo(recNo int) (int, error) {
	ctx, cancel := r.getWriteContext()
	defer cancel()

	var userID int
	err := r.db.QueryRow(ctx,
		"SELECT user_id FROM emulator.dahua_cards WHERE device_id = $1 AND rec_no = $2",
		r.deviceID, recNo).Scan(&userID)
	if err != nil {
		return 0, err
	}
	return userID, nil
}

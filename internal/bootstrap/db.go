package bootstrap

import (
	"fmt"
	stdlog "log"
	"net/url"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	log "github.com/sirupsen/logrus"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// postgresDurabilityDSN asks the server to skip the per-commit WAL flush for
// the connections this process opens.
//
// Every upload, copy and delete writes a row to the object metadata table, and a
// COMMIT that waits for fdatasync costs about 600 microseconds on ordinary disk:
// measured on one host with 8 concurrent clients, the metadata database turned
// 4 KiB uploads from 1661/s into 7964/s when the wait was dropped, while reads
// and large objects were untouched. The S3 layer is a thin view over files that
// already exist on the backend, so a power loss that loses the last few hundred
// milliseconds of metadata costs a metadata row, not object data: a re-upload or
// a rescan brings it back. That trade is worth making by default, and it stays
// reversible - a DSN that mentions synchronous_commit (for example
// "options='-c synchronous_commit=on'") is left exactly as given.
//
// Note this only relaxes the WAL flush of the database; object bytes are written
// through the storage driver and are not affected either way.
func postgresDurabilityDSN(dsn string) (string, bool) {
	const option = "-c synchronous_commit=off"
	// A DSN that mentions the setting anywhere keeps whatever it says: the
	// check is deliberately over-broad, because a false positive only costs
	// the speedup while a false negative would silently drop an operator's
	// explicit choice.
	if strings.Contains(dsn, "synchronous_commit") {
		return dsn, false
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return dsn, false
		}
		q := u.Query()
		if q.Has("options") {
			// An option list the operator wrote wins over ours.
			return dsn, false
		}
		q.Add("options", option)
		u.RawQuery = q.Encode()
		return u.String(), true
	}
	if hasKeywordOption(dsn) {
		// libpq also accepts "options = '...'" with blanks around the
		// equals sign; a substring scan would miss it and append a second
		// options keyword, whose last-wins parsing silently drops the
		// operator's list.
		return dsn, false
	}
	// libpq keyword form: the value holds spaces, so it has to be quoted.
	return dsn + " options='" + option + "'", true
}

// hasKeywordOption reports whether a libpq keyword/value DSN carries an options
// keyword. Tokens are separated by blanks, except inside single quotes where a
// blank is part of the value ("password='two words'"); "options = '...'" with
// blanks around the equals sign therefore yields the tokens "options", "=" and
// the quoted value, and only a token scan - not a substring search - sees it.
func hasKeywordOption(dsn string) bool {
	var cur strings.Builder
	inQuote := false
	found := false
	emit := func() {
		t := cur.String()
		if t == "options" || strings.HasPrefix(t, "options=") {
			found = true
		}
		cur.Reset()
	}
	for _, r := range dsn {
		switch {
		case r == '\'':
			inQuote = !inQuote
			cur.WriteRune(r)
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				emit()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		emit()
	}
	return found
}

func InitDB() {
	logLevel := logger.Silent
	if flags.Debug || flags.Dev {
		logLevel = logger.Info
	}
	newLogger := logger.New(
		stdlog.New(log.StandardLogger().Out, "\r\n", stdlog.LstdFlags),
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  logLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)
	gormConfig := &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			TablePrefix: conf.Conf.Database.TablePrefix,
		},
		Logger: newLogger,
	}
	var dB *gorm.DB
	var err error
	if flags.Dev {
		dB, err = gorm.Open(openSQLite("file::memory:?cache=shared"), gormConfig)
		conf.Conf.Database.Type = "sqlite3"
	} else {
		database := conf.Conf.Database
		switch database.Type {
		case "sqlite3":
			{
				if !(strings.HasSuffix(database.DBFile, ".db") && len(database.DBFile) > 3) {
					log.Fatalf("db name error.")
				}
				dB, err = gorm.Open(openSQLite(fmt.Sprintf("%s?_journal=WAL&_vacuum=incremental",
					database.DBFile)), gormConfig)
			}
		case "mysql":
			{
				dsn := database.DSN
				if dsn == "" {
					//[username[:password]@][protocol[(address)]]/dbname[?param1=value1&...&paramN=valueN]
					dsn = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&tls=%s",
						database.User, database.Password, database.Host, database.Port, database.Name, database.SSLMode)
				}
				dB, err = gorm.Open(mysql.Open(dsn), gormConfig)
			}
		case "postgres":
			{
				dsn := database.DSN
				if dsn == "" {
					if database.Password != "" {
						dsn = fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=%s TimeZone=Asia/Shanghai",
							database.Host, database.User, database.Password, database.Name, database.Port, database.SSLMode)
					} else {
						dsn = fmt.Sprintf("host=%s user=%s dbname=%s port=%d sslmode=%s TimeZone=Asia/Shanghai",
							database.Host, database.User, database.Name, database.Port, database.SSLMode)
					}
				}
				dsn, relaxed := postgresDurabilityDSN(dsn)
				if relaxed {
					log.Infof("postgres: relaxing synchronous_commit to off for this process's connections (every table, not only S3 metadata: a power loss may drop the last ~200ms of committed changes; set synchronous_commit in the DSN to keep full durability)")
				}
				dB, err = gorm.Open(postgres.Open(dsn), gormConfig)
			}
		default:
			log.Fatalf("not supported database type: %s", database.Type)
		}
	}
	if err != nil {
		log.Fatalf("failed to connect database:%s", err.Error())
	}
	db.Init(dB)
}

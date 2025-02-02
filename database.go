package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	log "github.com/sirupsen/logrus"
	"strings"
	"time"
)

var db sqlx.DB

const CurrentDbVersion = 2

func connect() {
	userStr := ""
	if conf.Server.DB.User != "" {
		userStr = conf.Server.DB.User
		if conf.Server.DB.Password != "" {
			userStr += ":" + conf.Server.DB.Password
		}
		userStr += "@"
	}
	connStr := "postgresql://" + userStr + conf.Server.DB.Host + "/" + conf.Server.DB.DbName + "?sslmode=disable"
	log.Debug("Connecting to db using " + connStr)

	dbConn, err := sqlx.Connect("postgres", connStr)
	if err != nil {
		panic(err)
	}
	log.Debug("Connected to db")
	db = *dbConn

	var dbVersion int
	err = db.Get(&dbVersion, `SELECT MAX(version) FROM schema_upgrades`)
	if err != nil {
		log.Info("Initially creating tables...")
		initTables()
		setDbVersion(CurrentDbVersion)
		dbVersion = CurrentDbVersion
	}
	if dbVersion != CurrentDbVersion {
		doDbUpgrade(dbVersion)
	}
}

//go:embed db.sql
var dbInitScript string

func initTables() {
	_, err := db.Exec(dbInitScript)
	if err != nil {
		panic("Failed to execute db init script")
	}
}

func doDbUpgrade(fromDbVersion int) {
	log.Debugf("Upgrading db from version %d", fromDbVersion)
	if fromDbVersion < 2 {
		_, err := db.Exec("ALTER TABLE admin_tokens ADD COLUMN note text")
		if err != nil {
			panic("Failed to upgrade db")
		}
		setDbVersion(2)
	}
}

func setDbVersion(dbVersion int) {
	log.Infof("DB is now at version %d", dbVersion)
	_, err := db.Exec("INSERT INTO schema_upgrades(version) VALUES ($1)", dbVersion)
	if err != nil {
		log.Panicf(
			"Failed to set db version! If future restarts fail, you might have to manually set the version to %d",
			dbVersion)
	}
}

// only used for reading
type dbModule struct {
	Name       string `db:"name"`
	Parameters string `db:"parameters"`
}

func dbProfileExists(profileName string) bool {
	var profileExists bool

	err := db.Get(&profileExists, `SELECT EXISTS (SELECT * FROM profile WHERE name = $1)`, profileName)
	log.Info("Exec'd" + "SELECT EXISTS (SELECT * FROM profile WHERE name = " + profileName + ")")
	fmt.Printf("%#v\n", profileExists)
	if err != nil {
		panic(err)
	}

	return profileExists
}

func dbListPublicProfiles() []string {
	var profiles []string

	err := db.Select(&profiles, `SELECT name FROM profile WHERE public=true`)
	if err != nil {
		panic(err)
	}

	return profiles
}

func dbListProfiles() []string {
	var profiles []string

	err := db.Select(&profiles, `SELECT name FROM profile`)
	if err != nil {
		panic(err)
	}

	return profiles
}

func dbReadProfile(profileName string) *profile {
	profile := new(profile)
	err := db.Get(profile, "SELECT name, source, public, immutable_past FROM profile WHERE name = $1", profileName)
	if err != nil {
		return nil
	} //profile does not exist
	err = db.Select(&profile.Tokens, "SELECT token FROM admin_tokens WHERE profile = $1", profileName)
	if err != nil {
		log.Fatal(err)
	}
	err = db.Select(&profile.NTokens, "SELECT token, note FROM admin_tokens WHERE profile = $1", profileName)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%#v\n", profile)

	var dbModules []dbModule
	err = db.Select(&dbModules, "SELECT name, parameters FROM module WHERE profile = $1", profileName)
	fmt.Printf("%#v\n", dbModules)
	if err != nil {
		log.Fatal(err)
	}
	for _, dbModule := range dbModules {
		moduleParameters := map[string]string{}
		err = json.Unmarshal([]byte(dbModule.Parameters), &moduleParameters)
		if err != nil {
			log.Fatal(err)
		}
		moduleParameters["name"] = dbModule.Name
		profile.Modules = append(profile.Modules, moduleParameters)
	}
	fmt.Printf("%#v\n", profile.Modules)
	return profile
}

// dbWriteProfile writes the profile to the db, silently overwriting if a profile with the same name exists.
func dbWriteProfile(profile profile) {
	_, err := db.NamedExec(
		`INSERT INTO profile (name, source, public, immutable_past) VALUES (:name, :source, :public, :immutable_past)
ON CONFLICT (name) DO UPDATE SET source = excluded.source, public = excluded.public,
immutable_past = excluded.immutable_past`,
		profile)
	if err != nil {
		log.Fatal(err)
		return
	}
}

// used for importing
func dbProfileModuleExists(profile profile, module map[string]string) bool {
	moduleCopy := make(map[string]string)
	for k, v := range module {
		moduleCopy[k] = v
	}

	name := moduleCopy["name"]
	delete(moduleCopy, "name")
	parametersJson, err := json.Marshal(moduleCopy)
	if err != nil {
		panic(err)
	}

	var moduleExists bool
	err = db.Get(
		&moduleExists, `SELECT EXISTS (SELECT * FROM module WHERE profile = $1 AND name = $2 AND parameters = $3)`,
		profile.Name, name, parametersJson)
	fmt.Printf("%#v\n", moduleExists)
	if err != nil {
		panic(err)
	}

	return moduleExists
}

func dbAddProfileModule(profile profile, module map[string]string) {
	name := module["name"]
	delete(module, "name")
	parametersJson, err := json.Marshal(module)
	if err != nil {
		panic(err)
	}
	_, err = db.Exec(
		`INSERT INTO module (profile, name, parameters) VALUES ($1, $2, $3)`,
		profile.Name, name, parametersJson)
	if err != nil {
		panic(err)
	}
}

// this is currently somewhat expensive, since we need to find the module by the full parameters
func dbRemoveProfileModule(profile profile, module map[string]string) {
	name := module["name"]
	delete(module, "name")
	parametersJson, err := json.Marshal(module)
	if err != nil {
		panic(err)
	}
	_, err = db.Exec(
		`DELETE FROM module WHERE profile=$1 AND name=$2 AND parameters=$3`,
		profile.Name, name, parametersJson)
	if err != nil {
		panic(err)
	}
}

func dbWriteProfileToken(profile profile, token string, note *string) {
	if len(token) != 64 {
		log.Fatal("Only 64-byte tokens are allowed!")
	}
	_, err := db.Exec(
		`INSERT INTO admin_tokens (profile, token, note) VALUES ($1, $2, $3)
ON CONFLICT (token) DO UPDATE SET note = excluded.note`,
		profile.Name, token, note)
	if err != nil {
		panic(err)
	}
}

func dbRemoveProfileToken(profile profile, token string) {
	//TODO: ignore profile passed here, tokens are unique
	_, err := db.Exec(
		`DELETE FROM admin_tokens WHERE profile=$1 AND token=$2`,
		profile.Name, token)
	if err != nil {
		panic(err)
	}
}

func dbNotifierExists(notifierName string) bool {
	var notifierExists bool

	err := db.Get(&notifierExists, `SELECT EXISTS (SELECT * FROM notifier WHERE name = $1)`, notifierName)
	if err != nil {
		panic(err)
	}

	return notifierExists
}

func dbListNotifiers() []string {
	var notifiers []string

	err := db.Select(&notifiers, `SELECT name FROM notifier`)
	if err != nil {
		panic(err)
	}

	return notifiers
}

func dbReadNotifier(notifierName string, fetchRecipients bool) (*notifier, error) {
	readNotifier := new(notifier)
	tx, err := db.Beginx()
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	_, err = tx.Exec("SET intervalstyle = 'iso_8601'")
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	err = tx.Get(readNotifier, "SELECT name, source, \"interval\" FROM notifier WHERE name = $1", notifierName)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
		return nil, err
	}

	duration, err :=
		time.ParseDuration(strings.ToLower(strings.Split(readNotifier.Interval, "T")[1]))
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	//fmt.Printf("%#v\n", duration.String())
	readNotifier.Interval = duration.String()

	if fetchRecipients {
		err = db.Select(
			&readNotifier.Recipients,
			`SELECT email FROM recipients
JOIN notifier_recipients nr ON email = nr.recipient
JOIN notifier ON nr.notifier = notifier.name WHERE nr.notifier = $1`,
			notifierName)
	}
	return readNotifier, nil
}

// dbWriteNotifier writes the notifier to the db, silently overwriting if a notifier with the same name exists.
// Important: Does not write the notifier recipients to db! This has to be done manually through dbAddNotifierRecipient!
func dbWriteNotifier(notifier notifier) {
	_, err := db.NamedExec(
		`INSERT INTO notifier (name, source, interval) VALUES (:name, :source, :interval)
ON CONFLICT (name) DO UPDATE SET source = excluded.source, interval = excluded.interval`,
		notifier)
	if err != nil {
		log.Fatal(err)
		return
	}
}

func dbDeleteNotifier(notifier notifier) {
	_, err := db.Exec(`DELETE FROM notifier WHERE name=$1`, notifier.Name)
	if err != nil {
		panic(err)
	}
}

func dbAddNotifierRecipient(notifier notifier, recipient string) {
	_, err := db.Exec(`INSERT INTO recipients (email) VALUES ($1) ON CONFLICT (email) DO NOTHING`, recipient)
	if err != nil {
		log.Fatal(err)
		return
	}
	_, err = db.Exec(
		`INSERT INTO notifier_recipients (notifier, recipient) VALUES ($1, $2)
ON CONFLICT (notifier, recipient) DO NOTHING`,
		notifier.Name, recipient)
	if err != nil {
		log.Fatal(err)
		return
	}
}

func dbRemoveNotifierRecipient(notifier notifier, recipient string) {
	_, err := db.Exec(`DELETE FROM notifier_recipients WHERE notifier = $1 AND recipient = $2`,
		notifier.Name, recipient)
	if err != nil {
		log.Fatal(err)
		return
	}
}

func dbRemoveRecipient(recipient string) {
	//It is too expensive to go through all cached notifiers and remove the recipient, we simply invalidate the cache.
	//(The db is much more efficient doing a cascading deletion)
	//TODO: invalidate cache
	_, err := db.Exec(`DELETE FROM recipients WHERE email = $1`, recipient)
	if err != nil {
		log.Fatal(err)
		return
	}
}

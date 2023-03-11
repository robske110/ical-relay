package main

import (
	"flag"
	"fmt"
	"github.com/fergusstrange/embedded-postgres"
	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

var version = "2.0.0-beta.DEV/branch:beta5_db_frontend"

var configPath string
var conf Config

var router *mux.Router

func main() {
	log.Info("Welcome to ical-relay, version " + version)

	var notifier string
	flag.StringVar(&notifier, "notifier", "", "Run notifier with given ID")
	flag.StringVar(&configPath, "config", "config.yml", "Path to config file")
	importData := flag.Bool("import-data", false, "Whether to import data")
	flag.Parse()

	// load config
	var err error
	conf, err = ParseConfig(configPath, *importData)
	if err != nil {
		os.Exit(1)
	}

	log.SetLevel(conf.Server.LogLevel)
	log.Debug("Debug log is enabled") // only shows if Debug is actually enabled

	// run notifier if specified
	if notifier != "" {
		log.Debug("Notifier mode called. Running: " + notifier)
		err := RunNotifier(notifier)
		if err != nil {
			os.Exit(1)
		} else {
			os.Exit(0)
		}
	} else {
		log.Debug("Server mode.")
	}

	if len(conf.Server.DB.Host) > 0 {
		// connect to DB
		if conf.Server.DB.Host == "Special:EMBEDDED" {
			log.Info("Starting embedded postgres server (this will take a while on the first run)...")
			if conf.Server.DB.User == "" {
				conf.Server.DB.User = "postgres"
			}
			if conf.Server.DB.Password == "" {
				conf.Server.DB.Password = "postgres"
			}
			postgres := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
				Username(conf.Server.DB.User).
				Password(conf.Server.DB.Password).
				Database(conf.Server.DB.DbName).
				Version(embeddedpostgres.V15).
				Logger(log.StandardLogger().Writer()).
				BinariesPath(conf.Server.StoragePath + "db/runtime").
				DataPath(conf.Server.StoragePath + "db/data").
				Locale("C").
				Port(5432)) //todo: support non default port
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
			go func() {
				sigs := <-sigs
				log.Info("Caught ", sigs)
				err := postgres.Stop()
				if err != nil {
					log.Fatal("Could not properly shutdown embedded postgres server: ", err)
				}
				os.Exit(0)
			}()
			err := postgres.Start()
			if err != nil {
				log.Fatal("Could not start embedded postgres server: ", err)
			}
			conf.Server.DB.Host = "localhost"
		}
		connect()
		fmt.Printf("%#v\n", db)

		if *importData {
			conf.importToDB()
		}
	}

	// setup template path
	templatePath = conf.Server.StoragePath + "templates/"
	htmlTemplates = template.Must(template.ParseGlob(templatePath + "*.html"))

	// setup routes
	router = mux.NewRouter()
	router.HandleFunc("/", indexHandler)
	router.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir(templatePath+"static/"))))
	router.HandleFunc("/view/{profile}/monthly", monthlyViewHandler).Name("monthlyView")
	router.HandleFunc("/view/{profile}/edit/{uid}", editViewHandler).Name("editView")
	router.HandleFunc("/view/{profile}/edit", modulesViewHandler).Name("modulesView")
	router.HandleFunc("/notifier/{notifier}/subscribe", notifierSubscribeHandler).Name("notifierSubscribe")
	router.HandleFunc("/notifier/{notifier}/unsubscribe", notifierUnsubscribeHandler).Name("notifierUnsubscribe")
	router.HandleFunc("/settings", settingsHandler).Name("settings")
	router.HandleFunc("/admin", adminHandler).Name("admin")
	router.HandleFunc("/profiles/{profile}", profileHandler).Name("profile")

	router.HandleFunc("/api/reloadconfig", reloadConfigApiHandler)
	router.HandleFunc("/api/calendars", calendarlistApiHandler)
	router.HandleFunc("/api/checkSuperAuth", checkSuperAuthorizationApiHandler)
	router.HandleFunc("/api/notifier/{notifier}/recipient", NotifyRecipientApiHandler).Name("notifier")
	router.HandleFunc("/api/profiles/{profile}/checkAuth", checkAuthorizationApiHandler).Name("apiCheckAuth")
	router.HandleFunc("/api/profiles/{profile}/calentry", calendarEntryApiHandler).Name("calentry")
	router.HandleFunc("/api/profiles/{profile}/modules", modulesApiHandler).Name("modules")
	router.HandleFunc("/api/profiles/{profile}/tokens", tokenEndpoint).Name("tokens")

	// start notifiers
	NotifierStartup()
	// start cleanup
	CleanupStartup()

	// start server
	address := conf.Server.Addr
	log.Info("Starting server on " + address)
	log.Fatal(http.ListenAndServe(address, router))
}

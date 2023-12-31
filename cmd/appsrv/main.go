package main

import (
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-jwt/jwt/v5"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"
	"golang.local/app-srv/app"
	"golang.local/app-srv/conf"
	"golang.local/app-srv/web"
	"golang.local/gc-c-db/db"
	"gopkg.in/yaml.v3"
	"log"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var (
	buildVersion = "develop"
	buildDate    = ""
)

func main() {
	log.Printf("[Main] Starting up Decide Quiz Master Server #%s (%s)\n", buildVersion, buildDate)
	y := time.Now()

	//Hold main thread till safe shutdown exit:
	wg := &sync.WaitGroup{}
	wg.Add(1)

	//Get working directory:
	cwdDir, err := os.Getwd()
	if err != nil {
		log.Println(err)
	}

	//Load environment file:
	err = godotenv.Load()
	if err != nil {
		log.Fatalln("Error loading .env file")
	}

	//Data directory processing:
	dataDir := os.Getenv("DIR_DATA")
	if dataDir == "" {
		dataDir = path.Join(cwdDir, ".data")
	}

	check(os.MkdirAll(dataDir, 0777))

	//Config file processing:
	configLocation := os.Getenv("CONFIG_FILE")
	if configLocation == "" {
		configLocation = path.Join(dataDir, "config.yml")
	} else {
		if !filepath.IsAbs(configLocation) {
			configLocation = path.Join(dataDir, configLocation)
		}
	}

	//Config loading:
	configFile, err := os.Open(configLocation)
	if err != nil {
		log.Fatalln("Failed to open config.yml")
	}

	var configYml conf.ConfigYaml
	groupsDecoder := yaml.NewDecoder(configFile)
	err = groupsDecoder.Decode(&configYml)
	if err != nil {
		log.Fatalln("Failed to parse config.yml:", err)
	}

	//DB Connection:
	manager := &db.Manager{Path: configYml.DBPath}
	err = manager.Connect()
	if err != nil {
		log.Fatalln("Failed to open DB connection:", err)
	}

	err = manager.AssureAllTables()
	if err != nil {
		log.Fatalln("Failed to assure DB schema:", err)
	}

	//Load keys:
	pubk, err := jwt.ParseRSAPublicKeyFromPEM([]byte(configYml.Identity.PublicKey))
	if err != nil && configYml.Identity.PublicKey != "" {
		log.Println("[WARN] Failed to decode public key:", err)
	}

	//Server definitions:
	log.Printf("[Main] Starting up HTTP server on %s...\n", configYml.Listen.Web)
	webServer, muxer := web.New(configYml)

	log.Println("[Main] Starting App Server Processing...")
	appServer := &app.Server{}
	appServer.Activate(configYml, pubk, muxer)

	//Safe Shutdown
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	//Startup complete:
	z := time.Now().Sub(y)
	log.Printf("[Main] Took '%s' to fully initialize modules\n", z.String())

	go func() {
		<-sigs
		fmt.Printf("\n")

		log.Printf("[Main] Attempting safe shutdown\n")
		a := time.Now()

		log.Printf("[Main] Shutting down HTTP server...\n")
		err := webServer.Close()
		if err != nil {
			log.Println(err)
		}

		log.Println("[Main] Shutting down App Server Processing...")
		err = appServer.Close()
		if err != nil {
			log.Println(err)
		}

		log.Printf("[Main] Signalling program exit...\n")
		b := time.Now().Sub(a)
		log.Printf("[Main] Took '%s' to fully shutdown modules\n", b.String())
		wg.Done()
	}()

	wg.Wait()
	log.Println("[Main] Goodbye")
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

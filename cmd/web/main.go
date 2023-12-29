package main

import (
	"database/sql"
	"encoding/gob"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"subscription/data"
	"sync"
	"syscall"
	"time"

	"github.com/alexedwards/scs/redisstore"
	"github.com/alexedwards/scs/v2"
	"github.com/gomodule/redigo/redis"
	_ "github.com/jackc/pgconn"
	_ "github.com/jackc/pgx/v4"
	_ "github.com/jackc/pgx/v4/stdlib"
)

const webPort = "80"

func main() {
	// connect to DB
	db := initDB()
	db.Ping()

	gob.Register(data.User{})

	// create sessions
	session := initSession()

	// create loggers
	infoLog := log.New(os.Stdout, "INFO\t", log.Ldate|log.Ltime)
	errorLog := log.New(os.Stdout, "ERROR\t", log.Ldate|log.Ltime|log.Lshortfile)

	// create wait group
	wg := sync.WaitGroup{}

	// set up application config
	app := Config{
		Session:  session,
		DB:       db,
		Wait:     &wg,
		InfoLog:  infoLog,
		ErrorLog: errorLog,
		Models:   data.New(db),
	}

	// set up mail
	app.Mailer = app.createMail()
	go app.listenForMail()

	// shutdown gracefully
	go app.listenForShutdown()

	// start webserver
	app.serve()

}

func (app *Config) serve() {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", webPort),
		Handler: app.routes(),
	}

	app.InfoLog.Println("Starting web server...")
	err := srv.ListenAndServe()
	if err != nil {
		log.Panicln(err)
	}
}

func initDB() *sql.DB {
	dbConn := connectToDB()
	if dbConn == nil {
		log.Panicln("Error connecting to the DB")
	}
	return dbConn
}

func connectToDB() *sql.DB {
	counts := 0
	for {
		connection, err := openDB()
		if err != nil {
			log.Println("DB is not ready...")
		} else {
			log.Println("Connected to database!")
			return connection
		}

		if counts >= 10 {
			return nil
		}

		log.Println("Backing off for a sec...")
		time.Sleep(1 * time.Second)
		counts++
	}
}

func openDB() (*sql.DB, error) {
	dsn := os.Getenv("DSN")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	return db, nil

}

func initSession() *scs.SessionManager {
	session := scs.New()
	session.Store = redisstore.New(initRedis())
	session.Lifetime = 24 * time.Hour
	session.Cookie.Persist = true
	session.Cookie.SameSite = http.SameSiteLaxMode
	session.Cookie.Secure = true

	return session
}

func initRedis() *redis.Pool {
	redisPool := &redis.Pool{
		MaxIdle: 10,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", os.Getenv("REDIS"))
		},
	}
	return redisPool
}

func (app *Config) listenForShutdown() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	app.shutdown()
	os.Exit(0)
}
func (app *Config) shutdown() {
	// perform clean up tasks
	app.InfoLog.Println("running clean up tasks...")

	// block until waitgroup is empty
	app.Wait.Wait()
	app.Mailer.DoneChan <- true

	close(app.Mailer.ErrorChan)
	close(app.Mailer.DoneChan)
	close(app.Mailer.MailerChan)

	app.InfoLog.Println("closing channels and shutting down application...")

}
func (app *Config) createMail() Mail {
	// create channels
	errorChan := make(chan error)
	mailerChan := make(chan Message, 100)
	mailerDoneChan := make(chan bool)

	m := Mail{
		Domain:      "localhost",
		Host:        "localhost",
		Port:        1025,
		Encryption:  "none",
		FromName:    "Info",
		FromAddress: "info@mycompany.com",
		Wait:        app.Wait,
		ErrorChan:   errorChan,
		MailerChan:  mailerChan,
		DoneChan:    mailerDoneChan,
	}

	return m
}

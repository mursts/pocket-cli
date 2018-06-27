package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"text/template"

	"errors"

	"github.com/motemen/go-pocket/api"
	"github.com/motemen/go-pocket/auth"
	"github.com/urfave/cli"
)

const (
	version = "0.1"
)

var defaultItemTemplate = template.Must(template.New("item").Parse(
	`[{{.ItemID | printf "%9d"}}] {{.Title}} <{{.URL}}>`,
))

var configDir string

func init() {
	usr, err := user.Current()
	if err != nil {
		panic(err)
	}

	configDir = filepath.Join(usr.HomeDir, ".config", "pocket")
	err = os.MkdirAll(configDir, 0777)
	if err != nil {
		panic(err)
	}
}

func main() {
	app := cli.NewApp()
	app.Name = "pocket-cli"
	app.Usage = "A Pocket command line client"
	app.Version = version

	formatFlag := cli.StringFlag{
		Name:  "format, f",
		Usage: "A Go template to show items.",
	}
	domainFlag := cli.StringFlag{
		Name:  "domain, d",
		Usage: "Filter items by its domain when listing.",
	}
	searchFlag := cli.StringFlag{
		Name:  "search, s",
		Usage: "Search query when listing.",
	}
	countFlag := cli.StringFlag{
		Name:  "count, c",
		Usage: "Only return count number of items.",
	}
	tagFlag := cli.StringFlag{
		Name:  "tag, t",
		Usage: "Filter items by a tag when listing.",
	}

	titleFlag := cli.StringFlag{
		Name:  "title, t",
		Usage: "A manually specified title for the article",
	}
	tagsFlag := cli.StringFlag{
		Name:  "tags, tg",
		Usage: "A comma-separated list of tags",
	}

	app.Before = func(c *cli.Context) error {
		consumerKey := getConsumerKey()

		accessToken, err := restoreAccessToken(consumerKey)
		if err != nil {
			panic(err)
		}

		client := api.NewClient(consumerKey, accessToken.AccessToken)

		app.Metadata = map[string]interface{}{
			"client": client,
		}

		return nil
	}

	app.Commands = []cli.Command{
		{
			Name:    "list",
			Aliases: []string{"l"},
			Usage:   "Show items",
			Action:  commandList,
			Flags: []cli.Flag{
				formatFlag,
				domainFlag,
				searchFlag,
				countFlag,
				tagFlag,
			},
		},
		{
			Name:    "add",
			Aliases: []string{"a"},
			Usage:   "Add item",
			Action:  commandAdd,
			Flags: []cli.Flag{
				titleFlag,
				tagsFlag,
			},
		},
		{
			Name:   "archive",
			Usage:  "Archive item",
			Action: commandArchive,
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

type bySortID []api.Item

func (s bySortID) Len() int           { return len(s) }
func (s bySortID) Less(i, j int) bool { return s[i].SortId < s[j].SortId }
func (s bySortID) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func commandList(c *cli.Context) error {
	options := &api.RetrieveOption{}

	if domain := c.String("domain"); domain != "" {
		options.Domain = domain
	}
	if search := c.String("search"); search != "" {
		options.Search = search
	}
	if tag := c.String("tag"); tag != "" {
		options.Tag = tag
	}
	options.Count = 10
	if count := c.String("count"); count != "" {
		if i, err := strconv.Atoi(count); err == nil {
			options.Count = i
		}
	}

	client := c.App.Metadata["client"].(*api.Client)

	res, err := client.Retrieve(options)
	if err != nil {
		return errors.New(fmt.Sprintf("failed to item retrieve. %v", err))
	}

	var itemTemplate *template.Template
	if format := c.String("format"); format != "" {
		itemTemplate = template.Must(template.New("item").Parse(format))
	} else {
		itemTemplate = defaultItemTemplate
	}

	var items []api.Item
	for _, item := range res.List {
		items = append(items, item)
	}

	sort.Sort(bySortID(items))

	for _, item := range items {
		err := itemTemplate.Execute(os.Stdout, item)
		if err != nil {
			panic(err)
		}
		fmt.Println("")
	}

	return nil
}

func commandArchive(c *cli.Context) error {
	itemIDString := c.Args().First()
	if itemIDString == "" {
		return errors.New("item id not found")
	}

	itemID, err := strconv.Atoi(itemIDString)
	if err != nil {
		return errors.New("item id should be number")
	}

	client := c.App.Metadata["client"].(*api.Client)

	action := api.NewArchiveAction(itemID)
	res, err := client.Modify(action)
	fmt.Println(res, err)

	return nil
}

func commandAdd(c *cli.Context) error {
	options := &api.AddOption{}

	url := c.Args().First()
	if url == "" {
		return errors.New("url not found")
	}

	options.URL = url

	if title := c.String("title"); title != "" {
		options.Title = title
	}

	if tags := c.String("--tags"); tags != "" {
		options.Tags = tags
	}

	client := c.App.Metadata["client"].(*api.Client)

	err := client.Add(options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	return nil
}

func getConsumerKey() string {
	consumerKeyPath := filepath.Join(configDir, "consumer_key")
	consumerKey, err := ioutil.ReadFile(consumerKeyPath)

	if err != nil {
		log.Printf("Can't get consumer key: %v", err)
		log.Print("Enter your consumer key (from here https://getpocket.com/developer/apps/): ")

		consumerKey, _, err = bufio.NewReader(os.Stdin).ReadLine()
		if err != nil {
			panic(err)
		}

		err = ioutil.WriteFile(consumerKeyPath, consumerKey, 0600)
		if err != nil {
			panic(err)
		}

		return string(consumerKey)
	}

	return string(bytes.SplitN(consumerKey, []byte("\n"), 2)[0])
}

func restoreAccessToken(consumerKey string) (*auth.Authorization, error) {
	accessToken := &auth.Authorization{}
	authFile := filepath.Join(configDir, "auth.json")

	err := loadJSONFromFile(authFile, accessToken)

	if err != nil {
		log.Println(err)

		accessToken, err = obtainAccessToken(consumerKey)
		if err != nil {
			return nil, err
		}

		err = saveJSONToFile(authFile, accessToken)
		if err != nil {
			return nil, err
		}
	}

	return accessToken, nil
}

func obtainAccessToken(consumerKey string) (*auth.Authorization, error) {
	ch := make(chan struct{})
	ts := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/favicon.ico" {
				http.Error(w, "Not Found", 404)
				return
			}

			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintln(w, "Authorized.")
			ch <- struct{}{}
		}))
	defer ts.Close()

	redirectURL := ts.URL

	requestToken, err := auth.ObtainRequestToken(consumerKey, redirectURL)
	if err != nil {
		return nil, err
	}

	url := auth.GenerateAuthorizationURL(requestToken, redirectURL)
	fmt.Println(url)

	<-ch

	return auth.ObtainAccessToken(consumerKey, requestToken)
}

func saveJSONToFile(path string, v interface{}) error {
	w, err := os.Create(path)
	if err != nil {
		return err
	}

	defer w.Close()

	return json.NewEncoder(w).Encode(v)
}

func loadJSONFromFile(path string, v interface{}) error {
	r, err := os.Open(path)
	if err != nil {
		return err
	}

	defer r.Close()

	return json.NewDecoder(r).Decode(v)
}

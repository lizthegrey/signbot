package signprocessor

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"github.com/mrjones/oauth"
	"github.com/zabawaba99/fireauth"
	"github.com/zabawaba99/firego"

	"golang.org/x/oauth2"
)

func EventLoop() {
	data := make(fireauth.Data)
	options := fireauth.Option{
		Admin: true,
	}
	token, _ := fireauth.New(Secrets.FireBaseSecret).CreateToken(data, &options)
	f := firego.New(Config.FireBaseDB, nil)
	f.Auth(token)

	t := oauth.NewConsumer(Secrets.TwitterKey, Secrets.TwitterSecret, oauth.ServiceProvider{
		RequestTokenUrl:   "https://api.twitter.com/oauth/request_token",
		AuthorizeTokenUrl: "https://api.twitter.com/oauth/authorize",
		AccessTokenUrl:    "https://api.twitter.com/oauth/access_token",
	})

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: Secrets.GithubToken},
	)
	tc := oauth2.NewClient(oauth2.NoContext, ts)
	gh := github.NewClient(tc)

	notifications := make(chan firego.Event)
	if err := f.Watch(notifications); err != nil {
		log.Fatalf("Error setting up watch: %v", err)
	}

	defer f.StopWatching()
	for event := range notifications {
		if event.Path == "/" && event.Data != nil {
			if users, ok := event.Data.(map[string]interface{})["users"]; ok {
				for uid, d := range users.(map[string]interface{}) {
					details := d.(map[string]interface{})
					if process(uid, details, t, gh) {
						f.Child("users").Child(uid).Remove()
					}
				}
			}
		} else if event.Type == "put" && strings.HasPrefix(event.Path, "/users/") {
			uid := strings.TrimPrefix(event.Path, "/users/")
			if details, ok := event.Data.(map[string]interface{}); ok {
				if process(uid, details, t, gh) {
					f.Child("users").Child(uid).Remove()
				}
			}
		}
	}
	fmt.Printf("Notifications have stopped\n")
}

func process(uid string, details map[string]interface{}, t *oauth.Consumer, gh *github.Client) bool {
	linkProfile := details["linkProfile"].(bool)
	link := details["link"].(string)
	personalPage := details["personalPage"].(string)
	name := details["name"].(string)
	title := details["title"].(string)
	affiliation := details["affiliation"].(string)

	secret := details["twitterSecret"].(string)
	token := details["twitterToken"].(string)
	tclient, err := t.MakeHttpClient(&oauth.AccessToken{Token: token, Secret: secret})
	resp, err := tclient.Get("https://api.twitter.com/1.1/account/verify_credentials.json?skip_status=true&include_entities=false")
	if err != nil {
		log.Printf("Twitter API error: %v processing %s", err, uid)
		return false
	}
	defer resp.Body.Close()

	bits, _ := ioutil.ReadAll(resp.Body)

	var user map[string]interface{}
	json.Unmarshal(bits, &user)
	egg := user["default_profile_image"].(bool)
	created, _ := time.Parse(time.RubyDate, user["created_at"].(string))
	description := user["description"].(string)
	followers := int(user["followers_count"].(float64))
	following := int(user["friends_count"].(float64))
	displayName := user["name"].(string)
	handle := user["screen_name"].(string)
	tweets := int(user["statuses_count"].(float64))
	var url string
	switch user["url"].(type) {
	case string:
		url = user["url"].(string)
	}

	if name == "" {
		log.Printf("No signatory name specified for %s (%s)", uid, handle)
		return false
	}

	if score := Score(handle, displayName, url, created, followers, following, tweets, egg, description, personalPage); score < 0 {
		log.Printf("Not creating pull for %s (%s) due to score %d", uid, handle, score)
		return false
	}

	var linkMd, affiliationMd, titleMd string
	if linkProfile {
		linkMd = fmt.Sprintf("  link: https://twitter.com/%s\n", handle)
	} else if link != "" {
		linkMd = fmt.Sprintf("  link: %s\n", link)
	}
	if affiliation != "" {
		affiliationMd = fmt.Sprintf("  affiliation: \"%s\"\n", affiliation)
	}
	if title != "" {
		titleMd = fmt.Sprintf("  occupation_title: \"%s\"\n", title)
	}
	contents := fmt.Sprintf("---\n  name: \"%s\"\n%s%s%s---", name, linkMd, affiliationMd, titleMd)

	body := fmt.Sprintf(
		"Twitter user: https://twitter.com/%s\n" +
		"Created: %v, Followers: %d, Following: %d, Tweets: %d, Egg: %v\n" +
		"\n" +
		"Twitter profile fields:\n" +
		"Name: %s\n" +
		"Website: %s\n" +
		"Tagline: %s\n" +
		"\n" +
		"Personal page: %s\n" +
		"\n" +
		"Signature file contents:\n" +
		"%s",
		handle,
		created, followers, following, tweets, egg,
		displayName,
		url,
		description,
		personalPage,
		"    " + String.Replace(contents, "\n", "\n    ")
	)


	// Ensure we are forking from a clean state.
	g := gh.Git
	ref, _, err := g.GetRef("neveragaindottech", "neveragaindottech.github.io", "heads/master")
	if err != nil {
		log.Printf("Error: %v processing %s", err, uid)
		return false
	}
	refStr := fmt.Sprintf("heads/%s", uid)
	ref.Ref = &refStr
	if _, _, err = g.UpdateRef(Config.GithubUser, "neveragaindottech.github.io", ref, true); err != nil {
		if _, _, err = g.CreateRef(Config.GithubUser, "neveragaindottech.github.io", ref); err != nil {
			log.Printf("Error: %v processing %s", err, uid)
			return false
		}
	}
	baseC, _, err := g.GetCommit(Config.GithubUser, "neveragaindottech.github.io", *ref.Object.SHA)
	if err != nil {
		log.Printf("Error: %v processing %s", err, uid)
		return false
	}
	path := fmt.Sprintf("_signatures/%s.md", uid)
	mode := "100644"
	kind := "blob"
	newT, _, err := g.CreateTree(Config.GithubUser, "neveragaindottech.github.io", *baseC.Tree.SHA, []github.TreeEntry{
		{Path: &path, Mode: &mode, Type: &kind, Content: &contents},
	})
	if err != nil {
		log.Printf("Error: %v processing %s", err, uid)
		return false
	}
	desc := fmt.Sprintf("SignBot: Add signatory '%s' (%s)", name, handle)
	newC, _, err := g.CreateCommit(Config.GithubUser, "neveragaindottech.github.io", &github.Commit{
		Message: &desc,
		Tree:    newT,
		Parents: []github.Commit{*baseC},
	})
	if err != nil {
		log.Printf("Error: %v processing %s", err, uid)
		return false
	}
	if _, _, err = g.UpdateRef(Config.GithubUser, "neveragaindottech.github.io", &github.Reference{
		Ref: &refStr,
		Object: &github.GitObject{
			SHA: newC.SHA,
		},
	}, false); err != nil {
		log.Printf("Error: %v processing %s", err, uid)
		return false
	}
	p := gh.PullRequests
	branchName := fmt.Sprintf("%s:%s", Config.GithubUser, uid)
	master := "master"
	_, _, err = p.Create("neveragaindottech", "neveragaindottech.github.io", &github.NewPullRequest{
		Title: &desc,
		Head:  &branchName,
		Base:  &master,
		Body:  &body,
	})
	if err != nil {
		log.Printf("Error: %v processing %s", err, uid)
		return false
	}
	fmt.Printf("Processed %s (https://twitter.com/%s)\n", uid, handle)
	return true
}

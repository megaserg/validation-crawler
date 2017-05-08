package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	urllib "net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

type crawlTask struct {
	url    *urllib.URL
	parent string
}

// Given HTML tag and attribute name, returns value of that attribute or error
// if no such attribute was found.
func findAttr(tag html.Token, name string) (string, error) {
	for _, attr := range tag.Attr {
		if attr.Key == name {
			return attr.Val, nil
		}
	}
	return "", errors.New("No " + name + " attribute found")
}

// Given a HTML body, parses it and returns a set of hrefs (as in <a href="...">)
// and a set of allowed #fragments that other pages can use when linking to this page.
func parseHTML(htmlReader io.Reader) (map[*urllib.URL]bool, map[string]bool) {

	foundHrefs := make(map[*urllib.URL]bool)
	allowedHashtags := make(map[string]bool)

	tokenizer := html.NewTokenizer(htmlReader)

	for {
		event := tokenizer.Next()

		switch {
		case event == html.ErrorToken:
			// End of the document, we're done
			return foundHrefs, allowedHashtags

		case event == html.StartTagToken:
			token := tokenizer.Token()

			id, errNoID := findAttr(token, "id")
			if errNoID == nil {
				allowedHashtags["#"+id] = true
			}

			isAnchor := token.Data == "a"
			if isAnchor {
				name, errNoName := findAttr(token, "name")
				if errNoName == nil {
					allowedHashtags["#"+name] = true
				}

				href, errNoHref := findAttr(token, "href")
				if errNoHref == nil {
					hrefURL, err := urllib.Parse(href)
					if err == nil {
						foundHrefs[hrefURL] = true
					} else {
						log.Printf("Unparseable href: %s\n", token.String())
					}
				}

				if errNoID != nil && errNoName != nil && errNoHref != nil {
					//log.Printf("Useless link %s\n", token.String())
				}
			}
		}
	}
}

// Given a link, tries to access it. If unsuccessful, submits an async error message.
// If successful and should recurse, parses the page and submits new crawl tasks.
func crawl(
	link crawlTask,
	tasksLatch *sync.WaitGroup,
	shouldRecurse bool,
	brokenLinkMessagesChan chan<- string,
	allowedHashLinksChan chan<- string,
	crawlTasksChan chan<- crawlTask) {

	defer tasksLatch.Done()

	url := link.url

	//log.Printf("Crawling %s\n", url.String())
	resp, err := http.Get(url.String())
	if err != nil {
		brokenLinkMessagesChan <- fmt.Sprintf("Failed to GET: %s on %s: %s", url.String(), link.parent, err.Error())
		return
	}

	if resp.StatusCode != 200 {
		brokenLinkMessagesChan <- fmt.Sprintf("Dead link: %s on %s: HTTP code %d", url.String(), link.parent, resp.StatusCode)
		return
	}

	if shouldRecurse {
		htmlReader := resp.Body
		defer htmlReader.Close()

		foundHrefs, allowedHashtags := parseHTML(htmlReader)

		baseURL := resp.Request.URL
		baseURLString := baseURL.String()

		for allowedHashtag := range allowedHashtags {
			allowedHashLinksChan <- baseURLString + allowedHashtag
		}

		for hrefURL := range foundHrefs {
			resolvedURL := baseURL.ResolveReference(hrefURL)
			tasksLatch.Add(1)
			crawlTasksChan <- crawlTask{resolvedURL, baseURLString}
		}
	}
}

// Whether a URL is a sub-URL of any of the seed URLs.
func isDescendant(url string, seedURLs []string) bool {
	for _, seedURL := range seedURLs {
		if strings.HasPrefix(url, seedURL) {
			return true
		}
	}
	return false
}

// Given a URL, removes the #fragment part.
func stripHashtagFromURL(url *urllib.URL) *urllib.URL {
	parsedURL, err := urllib.Parse(url.Scheme + "://" + url.Host + url.Path + url.RawQuery)
	if err != nil {
		log.Printf("Failed to parse: %s", err.Error())
	}
	return parsedURL
}

// Given a channel of tasks and a handler, invokes the handler on every task.
func eventLoop(
	crawlTasksChan <-chan crawlTask,
	handler func(crawlTask),
	doneChan chan<- bool) {

	for crawlTask := range crawlTasksChan {
		//log.Println("processing")
		handler(crawlTask)
	}

	doneChan <- true
}

// Given a channel of strings, builds a set of them and submits to the output channel.
// Optionally prints the strings upon receiving.
func accumulateChan(
	messagesChan <-chan string,
	shouldPrint bool,
	doneChan chan<- map[string]bool) {

	messages := make(map[string]bool)

	for message := range messagesChan {
		if shouldPrint {
			log.Println(message)
		}
		messages[message] = true
	}

	doneChan <- messages
}

// Whether the URL looks like URL and doesn't contain known-to-fail parts.
func isValidURL(url string) bool {
	return (strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) &&
		!strings.Contains(url, "_history") && !strings.Contains(url, "rbcommons")
}

// Whether the URL is a sub-URL of any of the seed URLs.
func shouldCrawlURL(url string, seedURLs []string) bool {
	return isDescendant(url, seedURLs)
}

func main() {

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s URL [URL...]\n", os.Args[0])
		os.Exit(1)
	}

	log.Println("Hello")

	seedURLs := os.Args[1:]

	// remember broken link messages
	brokenLinkMessagesChan := make(chan string)
	doneBrokenLinkMessagesChan := make(chan map[string]bool)
	go accumulateChan(brokenLinkMessagesChan, true, doneBrokenLinkMessagesChan)

	// remember allowed hash links
	allowedHashLinksChan := make(chan string)
	doneHashLinksChan := make(chan map[string]bool)
	go accumulateChan(allowedHashLinksChan, false, doneHashLinksChan)

	crawlTasksChan := make(chan crawlTask)
	var tasksLatch sync.WaitGroup

	visitedLinks := make(map[string]bool)
	foundLinksWithHashes := make(map[crawlTask]bool)
	ignoredURLs := make(map[string]bool)

	processFoundLink := func(task crawlTask) {
		url := task.url
		urlString := url.String()

		if isValidURL(urlString) {
			shouldCrawl := shouldCrawlURL(urlString, seedURLs)
			if shouldCrawl {
				if url.Fragment != "" && url.Fragment != "start-of-content" {
					foundLinksWithHashes[task] = true
				}
			}

			pageURL := stripHashtagFromURL(url)
			pageURLString := pageURL.String()
			if !visitedLinks[pageURLString] {
				visitedLinks[pageURLString] = true
				time.Sleep(100 * time.Millisecond)
				go crawl(task, &tasksLatch, shouldCrawl,
					brokenLinkMessagesChan, allowedHashLinksChan, crawlTasksChan)
			} else {
				tasksLatch.Done()
			}
		} else {
			ignoredURLs[urlString] = true
			tasksLatch.Done()
		}
	}

	// start processing
	eventLoopDoneChan := make(chan bool)
	go eventLoop(crawlTasksChan, processFoundLink, eventLoopDoneChan)

	// seed
	for _, url := range seedURLs {
		parsedURL, err := urllib.Parse(url)
		if err != nil {
			log.Printf("Failed to parse provided URL: %s", err.Error())
		}

		tasksLatch.Add(1)
		crawlTasksChan <- crawlTask{parsedURL, ""}
	}
	log.Println("Submitted all seed URLs")

	log.Println("Waiting for all tasks to be done")
	tasksLatch.Wait()
	log.Println("All tasks are done")

	log.Println("Stopping eventloop")
	close(crawlTasksChan)
	log.Println("Waiting for eventloop to exit")
	<-eventLoopDoneChan
	log.Println("Eventloop exited")

	log.Println("Stopping broken links processing")
	close(brokenLinkMessagesChan)
	log.Println("Waiting for broken links processing to exit")
	brokenLinkMessages := <-doneBrokenLinkMessagesChan
	log.Println("Broken links processing exited")

	log.Println("Stopping hashlinks processing")
	close(allowedHashLinksChan)
	log.Println("Waiting for hashlinks processing to exit")
	allowedHashLinks := <-doneHashLinksChan
	log.Println("Hashlink processing exited")

	log.Println("Crawled links:")
	for link := range visitedLinks {
		log.Println(link)
	}

	log.Println("Broken links:")
	for brokenLinkMessage := range brokenLinkMessages {
		log.Println(brokenLinkMessage)
	}

	log.Println("Broken hashlinks:")
	for task := range foundLinksWithHashes {
		url := task.url
		urlString := url.String()
		userContentModifiedStr := strings.Replace(urlString, "#", "#user-content-", 1)
		wikiModifiedStr := strings.Replace(urlString, "#wiki-", "#user-content-", 1)
		if !allowedHashLinks[urlString] && !allowedHashLinks[userContentModifiedStr] && !allowedHashLinks[wikiModifiedStr] {
			log.Printf("%s on %s", urlString, task.parent)
		}
	}

	//log.Println("Allowed Hashlinks:")
	//for x := range allowedHashLinks {
	//	log.Println(x)
	//}

	//log.Println("Ignored:")
	//for x := range ignoredURLs {
	//	log.Println(x)
	//}
}

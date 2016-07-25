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

type FoundLink struct {
	url    *urllib.URL
	parent string
}

func findAttr(token html.Token, key string) (string, error) {
	for _, attr := range token.Attr {
		if attr.Key == key {
			return attr.Val, nil
		}
	}
	return "", errors.New("No " + key + " attribute found")
}

func parseHTML(htmlReader io.Reader, base *urllib.URL, taskChan chan <- FoundLink, legalHashLinksChan chan <- string, tasks *sync.WaitGroup) {
	tokenizer := html.NewTokenizer(htmlReader)

	for {
		event := tokenizer.Next()

		switch {
		case event == html.ErrorToken:
			// End of the document, we're done
			return
		case event == html.StartTagToken:
			token := tokenizer.Token()

			isAnchor := token.Data == "a"
			if isAnchor {

				id, err_noid := findAttr(token, "id")
				if err_noid == nil {
					legalHashLinksChan <- base.String() + "#" + id
				}

				name, err_noname := findAttr(token, "name")
				if err_noname == nil {
					legalHashLinksChan <- base.String() + "#" + name
				}

				href, err_nohref := findAttr(token, "href")
				if err_nohref == nil {
					hrefURL, err := urllib.Parse(href)
					if err != nil {
						log.Printf("Unparseable href: %s\n", token.String())
					} else {
						resolvedURL := base.ResolveReference(hrefURL)

						tasks.Add(1)
						taskChan <- FoundLink{resolvedURL, base.String()}
					}
				}

				if err_noid != nil && err_noname != nil && err_nohref != nil {
					//log.Printf("Useless link %s\n", token.String())
				}
			} else {
				id, err_noid := findAttr(token, "id")
				if err_noid == nil {
					legalHashLinksChan <- base.String() + "#" + id
				}
			}
		}
	}
}

func crawl(task FoundLink, goDeeper bool, taskChan chan <- FoundLink, legalHashLinksChan chan <- string, tasks *sync.WaitGroup) {
	defer tasks.Done()

	url := task.url
	//log.Printf("Crawling %s\n", url.String())
	resp, err := http.Get(url.String())
	if err != nil {
		log.Printf("Failed to GET %s on %s: %s\n", url.String(), task.parent, err.Error())
		return
	}

	if resp.StatusCode != 200 {
		log.Printf("Dead link (%d) %s on %s\n", resp.StatusCode, url.String(), task.parent)
		return
	}

	if goDeeper {
		htmlReader := resp.Body
		defer htmlReader.Close()

		parseHTML(htmlReader, resp.Request.URL, taskChan, legalHashLinksChan, tasks)
	}
}

func isDescendant(url string, seedURLs []string) bool {
	for _, seedURL := range seedURLs {
		if strings.HasPrefix(url, seedURL) {
			return true
		}
	}
	return false
}

func extractPage(url *urllib.URL) *urllib.URL {
	parsedURL, err := urllib.Parse(url.Scheme + "://" + url.Host + url.Path + url.RawQuery)
	if err != nil {
		log.Printf("Failed to parse: %s", err.Error())
	}
	return parsedURL
}

func eventLoop(taskChan <-chan FoundLink, process func(FoundLink), finishedChan chan <- bool) {
	for task := range taskChan {
		//log.Println("processing")
		process(task)
	}

	log.Print("Done listening\n")
	finishedChan <- true
}

func acceptLegalHashLinks(legalHashLinksChan <-chan string, finishedChan chan <- map[string]bool) {
	legalHashLinks := make(map[string]bool)

	for legalHashLink := range legalHashLinksChan {
		legalHashLinks[legalHashLink] = true
	}

	finishedChan <- legalHashLinks
}

func main() {

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s URL [URL...]\n", os.Args[0])
		os.Exit(1)
	}

	log.Print("Hello\n")

	seedURLs := os.Args[1:]

	// accept hash links
	legalHashLinkChan := make(chan string)
	finishedHashLinksChan := make(chan map[string]bool)
	go acceptLegalHashLinks(legalHashLinkChan, finishedHashLinksChan)

	taskChan := make(chan FoundLink)
	var tasks sync.WaitGroup

	isValidURL := func(url string) bool {
		return (strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) &&
				!strings.Contains(url, "_history") && !strings.Contains(url, "rbcommons")
	}

	isCrawlableURL := func(url string) bool {
		return isDescendant(url, seedURLs)
	}

	visitedPages := make(map[string]bool)
	hashLinks := make(map[FoundLink]bool)
	ignoredURLs := make(map[string]bool)

	process := func(task FoundLink) {
		url := task.url
		urlString := url.String()

		if isValidURL(urlString) {
			isCrawlable := isCrawlableURL(urlString)
			if isCrawlable {
				if url.Fragment != "" && url.Fragment != "start-of-content" {
					hashLinks[task] = true
				}
			}

			pageURL := extractPage(url)
			pageURLString := pageURL.String()
			if !visitedPages[pageURLString] {
				visitedPages[pageURLString] = true
				time.Sleep(100 * time.Millisecond)
				go crawl(task, isCrawlable, taskChan, legalHashLinkChan, &tasks)
			} else {
				tasks.Done()
			}
		} else {
			ignoredURLs[urlString] = true
			tasks.Done()
		}
	}

	// start processing
	eventLoopFinishedChan := make(chan bool)
	go eventLoop(taskChan, process, eventLoopFinishedChan)

	// seed
	for _, url := range seedURLs {
		parsedURL, err := urllib.Parse(url)
		if err != nil {
			log.Printf("Failed to parse provided URL: %s", err.Error())
		}

		tasks.Add(1)
		taskChan <- FoundLink{parsedURL, ""}
	}
	log.Println("Submitted")

	tasks.Wait()

	close(taskChan)
	<-eventLoopFinishedChan

	close(legalHashLinkChan)
	legalHashLinks := <- finishedHashLinksChan
	log.Println("All done")

	//log.Println("Crawled:")
	//for x := range visitedPages {
	//	log.Println(x)
	//}
	log.Println("Broken Hashlinks:")
	for task := range hashLinks {
		url := task.url
		urlString := url.String()
		userContentModifiedStr := strings.Replace(urlString, "#", "#user-content-", 1)
		wikiModifiedStr := strings.Replace(urlString, "#wiki-", "#user-content-", 1)
		if !legalHashLinks[urlString] && !legalHashLinks[userContentModifiedStr] && !legalHashLinks[wikiModifiedStr] {
			log.Printf("%s on %s", urlString, task.parent)
		}
	}

	//log.Println("Legal Hashlinks:")
	//for x := range legalHashLinks {
	//	log.Println(x)
	//}

	//log.Println("Ignored:")
	//for x := range ignoredURLs {
	//	log.Println(x)
	//}
}

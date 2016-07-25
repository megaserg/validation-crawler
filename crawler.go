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

type foundLink struct {
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

func parseHTML(htmlReader io.Reader, base *urllib.URL, taskChan chan<- foundLink, legalHashLinksChan chan<- string, tasks *sync.WaitGroup) {
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

        id, errNoID := findAttr(token, "id")
        if errNoID == nil {
          legalHashLinksChan <- base.String() + "#" + id
        }

        name, errNoName := findAttr(token, "name")
        if errNoName == nil {
          legalHashLinksChan <- base.String() + "#" + name
        }

        href, errNoHref := findAttr(token, "href")
        if errNoHref == nil {
          hrefURL, err := urllib.Parse(href)
          if err != nil {
            log.Printf("Unparseable href: %s\n", token.String())
          } else {
            resolvedURL := base.ResolveReference(hrefURL)

            tasks.Add(1)
            taskChan <- foundLink{resolvedURL, base.String()}
          }
        }

        if errNoID != nil && errNoName != nil && errNoHref != nil {
          //log.Printf("Useless link %s\n", token.String())
        }
      } else {
        id, errNoID := findAttr(token, "id")
        if errNoID == nil {
          legalHashLinksChan <- base.String() + "#" + id
        }
      }
    }
  }
}

func crawl(task foundLink, goDeeper bool, taskChan chan<- foundLink, legalHashLinksChan chan<- string, tasks *sync.WaitGroup) {
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

func eventLoop(taskChan <-chan foundLink, process func(foundLink), finishedChan chan<- bool) {
  for task := range taskChan {
    //log.Println("processing")
    process(task)
  }

  log.Print("Done listening\n")
  finishedChan <- true
}

func acceptLegalHashLinks(legalHashLinksChan <-chan string, finishedChan chan<- map[string]bool) {
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

  taskChan := make(chan foundLink)
  var tasks sync.WaitGroup

  isValidURL := func(url string) bool {
    return (strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) &&
      !strings.Contains(url, "_history") && !strings.Contains(url, "rbcommons")
  }

  isCrawlableURL := func(url string) bool {
    return isDescendant(url, seedURLs)
  }

  visitedPages := make(map[string]bool)
  hashLinks := make(map[foundLink]bool)
  ignoredURLs := make(map[string]bool)

  process := func(task foundLink) {
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
    taskChan <- foundLink{parsedURL, ""}
  }
  log.Println("Submitted")

  tasks.Wait()

  close(taskChan)
  <-eventLoopFinishedChan

  close(legalHashLinkChan)
  legalHashLinks := <-finishedHashLinksChan
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

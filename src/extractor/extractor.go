package extractor

import (
    "blacklist"
    "env"
    "fmt"
    "h5"
    "hashie"
    "html/transform"
    "job"
    "kindlegen"
    "loggly"
    "net/http"
    "net/url"
    "os"
    "regexp"
    "sync"
    "util"
)

const DefaultAuthor = "Tinderizer"
const Readability = "https://readability.com/api/content/v1/parser"

type JSON map[string]interface{}

var token string
var notParsed *regexp.Regexp
var logger *loggly.Logger

func init() {
    token = env.Get("READABILITY_TOKEN")
    notParsed = regexp.MustCompile("(?i:Article Could not be Parsed)")
    logger = loggly.NewLogger("extractor", "Sorry, extraction failed.")
}

func buildReadabilityUrl(u string) string {
    return fmt.Sprintf("%s?url=%s&token=%s", Readability, url.QueryEscape(u), url.QueryEscape(token))
}

func downloadAndParse(j *job.Job) JSON {
    resp, err := http.Get(buildReadabilityUrl(j.Url.String()))
    if err != nil {
        logger.Fail("Readability Error: %s", err.Error())
    }
    defer resp.Body.Close()
    return util.ParseJSON(resp.Body, func(err error) {
        logger.Fail("JSON Parsing Error: %s", err.Error())
    })
}

func getImage(url string) *http.Response {
    resp, err := http.Get(url)
    if err != nil {
        panic(fmt.Sprintf("Failed download image %s: %s", url, err.Error()))
    }
    return resp
}

func downloadToFile(url, name string) {
    resp := getImage(url)
    defer resp.Body.Close()
    file, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        panic(fmt.Sprintf("Failed opening file: %s", err.Error()))
    }
    defer file.Close()
    util.Pipe(file, resp.Body, resp.ContentLength, func(err error) {
        panic(fmt.Sprintf("Error with io.Copy: %s", err.Error()))
    })
}

func rewriteAndDownloadImages(j *job.Job, doc *h5.Node) *h5.Node {
    var wg sync.WaitGroup
    root := j.Root()
    t := transform.NewTransform(doc)
    fix := transform.TransformAttrib("src", func(uri string) string {
        altered := fmt.Sprintf("%x.jpg", hashie.Sha1([]byte(uri)))
        wg.Add(1)
        go logger.SwallowError(func() {
            defer wg.Done()
            downloadToFile(uri, fmt.Sprintf("%s/%s", root, altered))
        })
        return altered
    })
    t.Apply(fix, "img")
    wg.Wait()
    return t.Doc()
}

func parseHTML(content string) *h5.Node {
    doc, err := transform.NewDoc(content)
    if err != nil {
        logger.Fail("HTML Parsing Error: %s", err.Error())
    }
    return doc
}

func makeRoot(j *job.Job) {
    if err := os.MkdirAll(j.Root(), 0755); err != nil {
        logger.Fail("Failed to make working directory: %s", err.Error())
    }
}

func checkDoc(data JSON, j *job.Job) {
    if data["error"] != nil && data["error"].(bool) {
        blacklist.Blacklist(j.Url.String())
        logger.Fail("Readability failed: %s", data["messages"].(string))
    }
    if notParsed.MatchString(data["title"].(string)) {
        blacklist.Blacklist(j.Url.String())
        logger.Fail("Readability failed, article could not be parsed.")
    }
}

func Extract(j *job.Job) {
    go logger.SwallowErrorAndNotify(j, func() {
        makeRoot(j)
        data := downloadAndParse(j)
        checkDoc(data, j)
        doc := parseHTML(data["content"].(string))
        j.Doc = rewriteAndDownloadImages(j, doc)
        j.Title = data["title"].(string)
        j.Domain = data["domain"].(string)
        author := data["author"]
        if author == nil {
            j.Author = DefaultAuthor
        } else {
            j.Author = author.(string)
        }
        j.Progress("Extraction complete...")
        kindlegen.Convert(j)
    })
}
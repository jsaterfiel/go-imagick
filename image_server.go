// Image Server
// Requirements:
// - Imagemagick libs installed on machine with latest version
// - Redis server running locally with the standard ports on the latest version
//
// You will want to install these Go packages:
// go get gopkg.in/gographics/imagick.v2/imagick  (see: https://github.com/gographics/imagick)
// go get gopkg.in/redis.v4 (see: https://github.com/go-redis/redis)
package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/gographics/imagick.v2/imagick"
	redis "gopkg.in/redis.v4"
)

const baseDir = "/projects/img"

const remoteImgURL = "http://mtv.mtvnimages.com/uri/"

const redisKeyPrefix = "imageServer_"

//5 second timeout
const imageFetchTimeout = time.Duration(5) * time.Second

const imageNotFoundPath = "/cc_missing_v6.jpg"

// uri/mgid:file:gsp:entertainment-assets:/mtv/arc/images/news/DailyNewsHits/photos/160512_YACHT_SOCIAL_thumbnail.png
const helpMsg = `<pre style="font-family:monospace">
========================================================================================================================
| Help
========================================================================================================================

How to make an url:
------------------------------------------------------------------------------------------------------------------------
http://localhost:8080/uri/{your parameters separated by colons}/{image mgid string}
Example:
http://localhost:8080/uri/rw=480:rh=320:ch=600:cw=800:cx=200:cy=200:q=50/mgid:file:gsp:entertainment-assets:/cc/images/shows/tds/videos/season_21/21095/ds_21_095_act2.jpg


Resize Parameters: (only need one of the parameters)
------------------------------------------------------------------------------------------------------------------------
rw - Resize width in pixels
rh - Resize height in pixels


Crop Parameters: (both crop width and crop height are required)
------------------------------------------------------------------------------------------------------------------------
cw - Crop width in pixels
ch - Crop height in pixels
cx - Crop x offset in pixels from top left as 0,0. Default is 0.
cy - Crop y offset in pixels from top left as 0,0. Default is 0.
cc - Crop to the center of the image.  Overrides cx and cy when used. true(1) false(0)  Default is false;


Quality Parameters: (if not passed no quality processing occurs.  Does nothing for gifs.)
------------------------------------------------------------------------------------------------------------------------
q - Quality, can be 0.5 or 50 but 1 is just 1 out of 100.
f - Format, can be either jpg, png or webp(lossy).  Does nothing for animated gifs
n - Normalize, enhances the contrast of a color image by adjusting the pixels color to span the entire range of colors available on all channels.  Not available on gifs.  true(1) false(0)  Default is false;


Animation Mode Parameters: (params used for handling animated gifs)
------------------------------------------------------------------------------------------------------------------------
am=s - Get still image (ie first frame of animated gif)
am=p - Get animated gif in preview mode which reduces the frames to 5 and sets the delay per frame to 1.5 seconds


Debugging Parameters:
------------------------------------------------------------------------------------------------------------------------
help - Returns this page
debug - Returns information about how the requested url was processed. No image is returned.
</pre>`

type parametersData struct {
	rw uint
	rh uint
	cw uint
	ch uint
	cx int
	cy int
	cc bool
	q  uint
	f  string
	n  bool
	am string
}

var mgidPatterns = struct {
	patterns map[string]string
}{
	patterns: map[string]string{
		"mgid:file:gsp:entertainment-assets:": baseDir,
	},
}

var redisClient *redis.Client

func init() {
	fmt.Println("Init called")
	imagick.Initialize()
	defer imagick.Terminate()

	redisClient = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	pong, err := redisClient.Ping().Result()
	fmt.Println(pong, err)
}

func handlerHelp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, helpMsg)
}

func parseUint(s string) uint {
	i, err := strconv.ParseUint(s, 10, 0)
	if err == nil {
		return uint(i)
	}
	fmt.Println("Failed to convert number: ", s)
	return uint(0)
}

func findParams(s string, m string, p *parametersData) {
	for _, v := range strings.Split(s, ":") {
		nv := strings.Split(v, "=")
		if len(nv) != 2 {
			continue
		}
		switch nv[0] {
		case "rw":
			p.rw = parseUint(nv[1])
		case "rh":
			p.rh = parseUint(nv[1])
		case "cw":
			p.cw = parseUint(nv[1])
		case "ch":
			p.ch = parseUint(nv[1])
		case "cx":
			i, err := strconv.Atoi(nv[1])
			if err == nil {
				p.cx = i
			}
		case "cy":
			i, err := strconv.Atoi(nv[1])
			if err == nil {
				p.cy = i
			}
		case "cc":
			p.cc = nv[1] == "1"
		case "q":
			p.q = parseUint(nv[1])
		case "f":
			p.f = nv[1]
		case "n":
			p.n = nv[1] == "1"
		case "am":
			p.am = nv[1]
		default:
			fmt.Printf("Unknown Parameter=%s for mgid=%s\n", nv[0], m)
		}
		fmt.Println("Found param: ", nv)
	}
}

// p is the path to the image
func setImageFetchLock(p string) bool {
	k := redisKeyPrefix + p
	v := redisClient.SetNX(k, "true", imageFetchTimeout)
	if v.Err() != nil {
		fmt.Println("Unsable to set the key: ", k, v.Err())
		panic("Error communicating with Redis")
	}
	return v.Val()
}

func getImageExtension(i string) string {
	return i[strings.LastIndex(i, ".")+1:]
}

func loadMissingImage(mw *imagick.MagickWand) {
	p := baseDir + imageNotFoundPath
	err := mw.ReadImage(p)
	if err != nil {
		fmt.Println("Error opening file:", p)
		panic(err)
	}
}

func fetchRemoteImageURL(m string, p string, mw *imagick.MagickWand) {
	url := remoteImgURL + m
	fmt.Println("Remote fetch Image: ", url)
	//try to remotely fetch the image
	resp, err := http.Get(url)
	defer resp.Body.Close()

	if err != nil {
		fmt.Printf("Error remote url fetch, path=%s", url)
		loadMissingImage(mw)
		return
	}

	i, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Failed to fetch remote image: ", url)
		loadMissingImage(mw)
		return
	}

	fmt.Println("Fetched Remote Image: ", url)

	//get image folder path
	ifi := strings.LastIndex(p, "/")
	ifp := p[:ifi]

	fmt.Println("Creating Directories: ", ifp)

	//write out the image to file for future usage
	err = os.MkdirAll(ifp, 0777)
	if err != nil {
		fmt.Println("Failed to create new directories for path: ", p)
		panic(err)
	}
	f, err := os.Create(p)

	defer f.Close()
	if err != nil {
		fmt.Println("Failed to create file: ", p)
		panic(err)
	}

	ib, err := f.Write(i)
	fmt.Println("Bytes written to file: ", p, ib)
	if ib < 1 && err != nil {
		fmt.Println("Failed to write to file: ", p)
		if err != nil {
			panic(err)
		}
	}

	mw.ReadImageBlob(i)
}

func handlerImageURI(w http.ResponseWriter, r *http.Request) {
	//params init
	var params parametersData
	params.cc = false
	params.n = false

	qs := r.URL.Query()

	if _, ok := qs["help"]; ok {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, helpMsg)
		return
	}

	//remove the original prefix of the path which is always 5 characters as it's uri/
	po := r.URL.Path[5:]
	//path to image
	var p string
	//mgid
	var m string

	switch strings.Index(po, "mgid:") {
	default:
		fmt.Println("Params found for path: ", po)
		//has parameters
		mi := strings.Index(po, "/")
		nv := po[:mi]
		m = po[mi+1:]
		//parse params
		fmt.Printf("index=%d mgid=%s, params=%s\n", mi, m, nv)
		findParams(nv, m, &params)
	case -1:
		fmt.Fprint(w, "404: Invalid image request")
		return
	case 0:
		fmt.Println("No params found for path: ", po)
		m = po
	}

	for mpi, mpv := range mgidPatterns.patterns {
		if strings.Index(m, mpi) > -1 {
			p = strings.Replace(m, mpi, mpv, 1)
		}
	}

	if p == "" {
		fmt.Println("Invalid mgid requested: ", m)
		fmt.Fprint(w, "404: Invalid mgid image requested")
		return
	}

	fmt.Printf("File Path: %s\n", p)

	mw := imagick.NewMagickWand()

	//check if the file exists
	if _, err := os.Stat(p); err == nil {
		err := mw.ReadImage(p)
		if err != nil {
			fmt.Println("Error opening file:", p)
			panic(err)
		}

		fmt.Println("Found image locally: ", p)
	}

	//check if file doesn't exist
	if _, err := os.Stat(p); os.IsNotExist(err) {
		//see if any other process is fetching the image.  if so then return 404 for now.
		if setImageFetchLock(p) == true {
			fetchRemoteImageURL(m, p, mw)
		} else {
			loadMissingImage(mw)
			p = baseDir + imageNotFoundPath
		}
	}

	//get extension
	ext := getImageExtension(p)
	fmt.Printf("Extension is: %s for path: %s\n", ext, p)

	//Handle image format change
	ah := r.Header.Get("Accept")
	fmt.Println("Header: ", ah)
	if strings.Contains(ah, "image/webp") {
		params.f = "webp"
		fmt.Println("Browser requested webp for image: ", p)
	}
	if params.f != "" && params.f != ext {
		fmt.Println("Format set to: ", params.f, "for path: ", p)
		mw.SetImageFormat(params.f)
		mw.SetFormat(params.f)
	}

	//CoalesceImages to break image into layers.  Must be called before any image layer specific operations.
	mw = mw.CoalesceImages()

	//Handle Crop
	if params.cw > 0 && params.ch > 0 {
		for i := 0; i < int(mw.GetNumberImages()); i++ {
			mw.SetIteratorIndex(i)
			x := params.cx
			y := params.cy
			if params.cc {
				//calculate the x and y for the offset
				// need to fix issue with trying to do math on uint values and how to cast to int from uint
				x = (int(mw.GetImageWidth()) - int(params.cw)) / 2
				y = (int(mw.GetImageHeight()) - int(params.ch)) / 2
			}
			fmt.Println("Crop image: ", p, " x=", x, ", y=", y)
			mw.CropImage(params.cw, params.ch, x, y)
			mw.SetImagePage(params.cw, params.ch, 0, 0)
		}
	}

	//Handle Resize
	if params.rw > 0 || params.rh > 0 {
		fmt.Printf("Resizing image: %s, width: %d height: %d", m, params.rw, params.rh)
		for i := 0; i < int(mw.GetNumberImages()); i++ {
			mw.SetIteratorIndex(i)
			mw.ThumbnailImage(params.rw, params.rh)
			mw.SetImagePage(params.rw, params.rh, 0, 0)
		}
	}

	//DeconstructImages after all resize and other image layer specifc operations
	mw = mw.DeconstructImages()

	//Handle Normalize
	if params.n {
		mw.NormalizeImageChannel(imagick.CHANNELS_ALL)
	}

	if params.f != "" {
		ext = params.f
	}

	//Handle Quality
	if params.q > 0 {
		switch ext {
		case "jpg":
			mw.SetImageCompression(imagick.COMPRESSION_JPEG)
		case "jpeg":
			mw.SetImageCompression(imagick.COMPRESSION_JPEG)
		case "png":
			mw.SetImageCompression(imagick.COMPRESSION_LOSSLESS_JPEG)
		case "webp":
			mw.SetImageCompression(imagick.COMPRESSION_LOSSLESS_JPEG)
		}
		mw.SetImageCompressionQuality(params.q)
	}

	mw.StripImage()

	w.Header().Set("Content-Type", "image/"+ext)
	w.Write(mw.GetImageBlob())
}

func main() {
	http.HandleFunc("/uri/", handlerImageURI)
	http.HandleFunc("/", handlerHelp)
	http.ListenAndServe(":8080", nil)
}

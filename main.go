// Image Server
// Requirements:
// - Imagemagick libs installed on machine with latest version of 6 I have ImageMagick 6.9.8-10 Q16
// - Redis server running locally with the standard ports on the latest version
//
// You will want to install these Go packages:
// go get gopkg.in/gographics/imagick.v3/imagick  (see: https://github.com/gographics/imagick)
// go get gopkg.in/redis.v4 (see: https://github.com/go-redis/redis)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"gopkg.in/gographics/imagick.v3/imagick"
	redis "gopkg.in/redis.v4"
)

var imgBaseDir string

var remoteImgURL string

const redisKeyLockPrefix = "imageServer_lock_"
const redisKeyCachePrefix = "imageServer_cache_"
const redisKeyCacheFormatPrefix = "imageServer_cache_format_"
const redisKeyCacheObjectPrefix = "imageServer_cache_object_"

//5 second timeout
const imageFetchTimeout = time.Duration(5) * time.Second

//5 minute timeout
const imageCacheTimeout = time.Duration(5) * time.Minute

//1 hour timeout
const objectCacheTimeout = time.Duration(1) * time.Hour

var imageNotFoundPath string

var imageIDQuery string

var redisADDR string

var redisClient *redis.Client

//var s3Client *s3.S3

const imageIDQueryString = "jp/[NAMESPACE]?&q={%22select%22:{%22virtualImageParams%22:{%22*%22:1},%22imageAssetRefs%22:{%22height%22:1,%22width%22:1,%22URI%22:1},%22ImagesWithCaptions%22:{%22Image%22:{%22virtualImageParams%22:{%22*%22:1},%22imageAssetRefs%22:{%22height%22:1,%22width%22:1,%22URI%22:1}}},%22virtualImageParams%22:{%22*%22:1},%22Images%22:{%22virtualImageParams%22:{%22*%22:1},%22imageAssetRefs%22:{%22height%22:1,%22width%22:1,%22URI%22:1}}},%22vars%22:{},%22where%22:{%22byId%22:[%22[KEYID]%22]},%22start%22:0,%22rows%22:1,%22omitNumFound%22:true,%22debug%22:{}}&stage=authoring&filterSchedules=true&dateFormat=UTC"

// uri/mgid:file:gsp:entertainment-assets:/mtv/arc/images/news/DailyNewsHits/photos/160512_YACHT_SOCIAL_thumbnail.png
const helpMsg = `<pre style="font-family:monospace">
========================================================================================================================
| Help
========================================================================================================================

How to make an url with uri:
------------------------------------------------------------------------------------------------------------------------
/uri/{your parameters separated by colons}/{image mgid string}
Example:
/uri/rw=480:rh=320:ch=600:cw=800:cx=200:cy=200:q=50/mgid:file:gsp:entertainment-assets:/cc/images/shows/tds/videos/season_21/21095/ds_21_095_act2.jpg

How to make an url with image id:
------------------------------------------------------------------------------------------------------------------------
/oid/{your parameters separated by colons}/{image mgid string}
Example:
/oid/rw=480:rh=320:q=50/mgid:arc:series:comedycentral.com:7c2d44b4-c8b1-43a9-9bfc-32af988eab20
/oid/rw=1323:rh=744:q=90/mgid:arc:video:comedycentral.com:2b469942-7bba-4d3a-9393-e9355f710d2c
/oid/rw=1920:rh=1080:q=90/mgid:arc:video:comedycentral.com:2b469942-7bba-4d3a-9393-e9355f710d2c
/oid/rw=500:rh=500:q=90/mgid:arc:video:comedycentral.com:2b469942-7bba-4d3a-9393-e9355f710d2c
/oid/rw=384:rh=260:q=90/mgid:arc:video:comedycentral.com:2b469942-7bba-4d3a-9393-e9355f710d2c
/oid/rw=1920:rh=1080:q=90/mgid:arc:video:comedycentral.com:7c2d44b4-c8b1-43a9-9bfc-32af988eab20
691 461

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


Query String Parameters:
------------------------------------------------------------------------------------------------------------------------
help - Returns this page
debug - Returns information about how the requested url was processed. No image is returned
cacheRefresh - clears the cache for this image request and fetches the image from the remote url
</pre>`

type parametersData struct {
	rw           uint
	rh           uint
	cw           uint
	ch           uint
	cx           int
	cy           int
	cc           bool
	q            uint
	f            string
	n            bool
	am           string
	cacheRefresh bool
	debug        bool
	msgs         []string
}

func (pd *parametersData) log(msg string) {
	fmt.Println(msg)
	if pd.debug {
		pd.msgs = append(pd.msgs, msg)
	}
}

type responseWrapper struct {
	response struct {
		docs []json.RawMessage
	}
}

type imageFormat struct {
	typeName string
}

type imageAssetRefs struct {
	format imageFormat
	height uint
	width  uint
	uri    string
}

type virtualImageParams struct {
	topLeftX       int
	topLeftY       int
	cropSizeWeight uint
	cropSizeHeight uint
}

type image struct {
	imageAssetRefs     []imageAssetRefs
	virtualImageParams []virtualImageParams
}

type item struct {
	images             []image
	imagesWithCaptions []struct {
		image image
	}
	image
}

func handlerHelp(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, helpMsg)
}

func parseUint(s string) uint {
	i, err := strconv.ParseFloat(s, 10)
	if err == nil {
		if i < 1 {
			i = i * 100
		}
		return uint(i)
	}
	fmt.Println("Failed to convert number: ", s)
	return uint(0)
}

func findParams(s string, m string, pd *parametersData) {
	for _, v := range strings.Split(s, ":") {
		nv := strings.Split(v, "=")
		if len(nv) != 2 {
			continue
		}
		switch nv[0] {
		case "rw":
			pd.rw = parseUint(nv[1])
		case "rh":
			pd.rh = parseUint(nv[1])
		case "cw":
			pd.cw = parseUint(nv[1])
		case "ch":
			pd.ch = parseUint(nv[1])
		case "cx":
			i, err := strconv.Atoi(nv[1])
			if err == nil {
				pd.cx = i
			}
		case "cy":
			i, err := strconv.Atoi(nv[1])
			if err == nil {
				pd.cy = i
			}
		case "cc":
			pd.cc = nv[1] == "1"
		case "q":
			pd.q = parseUint(nv[1])
		case "f":
			pd.f = nv[1]
		case "n":
			pd.n = nv[1] == "1"
		case "am":
			pd.am = nv[1]
		default:
			fmt.Println("Unknown Parameter=", nv[0], ", for mgid=", m)
			pd.log("Unknown Parameter: " + nv[0])
		}
		pd.log("found param: " + nv[0] + "=" + nv[1])
	}
}

func uintToString(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}

func intToString(v int) string {
	return strconv.FormatInt(int64(v), 10)
}

func floatToString(v float64) string {
	return strconv.FormatFloat(float64(v), 'E', 3, 10)
}

// p is the path to the image
func setImageFetchLock(p string, pd *parametersData) bool {
	k := redisKeyLockPrefix + p
	v := redisClient.SetNX(k, "true", imageFetchTimeout)
	if v.Err() != nil {
		fmt.Println("Unable to set the key: ", k, v.Err())
		pd.log("Unable to set the key: " + k + "; ERROR: " + v.Err().Error())
	}
	return v.Val()
}

// i is image path
// f is the requested format (if any)
// ha is header accept string
// ac is has alpha channel
func getImageFormat(i string, f string, ha string, ac bool, pd *parametersData) string {
	if f != "" {
		return strings.ToLower(f)
	}

	of := strings.ToLower(i[strings.LastIndex(i, ".")+1:])

	//Handle image format change
	if strings.Contains(ha, "image/webp") {
		of = "webp"
		pd.log("browser accepts webp changing image format to webp")
	} else if of != "gif" && !ac || of == "jpeg" {
		pd.log("changing image format to jpeg")
		of = "jpg"
	}

	return of
}

func loadMissingImage(mw *imagick.MagickWand, pd *parametersData) {
	fp := imgBaseDir + imageNotFoundPath
	//check if the file exists
	i, err := ioutil.ReadFile(fp)
	if err != nil || i == nil {
		//file not found locally fetch remote
		//see if any other process is fetching the image.  if so then return 404 for now.
		err = nil
		if setImageFetchLock(fp, pd) {
			fetchRemoteImageURL(imageNotFoundPath, imageNotFoundPath, pd, mw)
		} else {
			fmt.Println("Cannot load default image", fp)
			panic("Cannot load default image")
		}
	} else {
		pd.log("Found image locally: " + fp)
		err := mw.ReadImageBlob(i)
		if err != nil {
			fmt.Println("Error while reading image blob", err)
			pd.log("Error while reading image blob: " + err.Error())
		}
	}
}

func fetchRemoteImageURL(m string, p string, pd *parametersData, mw *imagick.MagickWand) {
	url := remoteImgURL + m + "?q=.9"
	pd.log("Remote fetch Image: " + url)
	//try to remotely fetch the image
	resp, err := http.Get(url)

	if err != nil {
		fmt.Println("Error remote url fetch, path: ", url)
		pd.log("Error remote url fetch, path: " + url)
		loadMissingImage(mw, pd)
		return
	}

	defer resp.Body.Close()

	i, err := ioutil.ReadAll(resp.Body)
	if err != nil || i == nil {
		fmt.Println("Failed to fetch remote image: ", url, err)
		pd.log("Failed to fetch remote image: " + err.Error())
		loadMissingImage(mw, pd)
		return
	}

	pd.log("Fetched Remote Image: " + url)

	//get image folder path
	ifi := strings.LastIndex(p, "/")
	ifp := imgBaseDir + p[:ifi]

	pd.log("Creating Directories: " + ifp)

	//write out the image to file for future usage
	err = os.MkdirAll(ifp, 0777)
	if err != nil {
		fmt.Println("Failed to create new directories for path: ", ifp, err)
		panic(err)
	}

	ip := imgBaseDir + p
	f, err := os.Create(ip)

	if err != nil {
		fmt.Println("Failed to create file: ", ip, err)
		panic(err)
	} else {
		defer f.Close()
	}

	ib, err := f.Write(i)
	pd.log("Bytes written to file: " + fmt.Sprint(ib))
	if ib < 1 && err != nil {
		fmt.Println("Failed to write to file: ", ip)
		pd.log("Failed to write to file: " + err.Error())
		panic(err)
	}
	mw.ReadImageBlob(i)
	//mw.ReadImageFile(f)
}

func saveImageInS3(path string, data []byte, pd *parametersData) {
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String("us-east-1")},
	)
	svc := s3manager.NewUploader(sess)
	input := &s3manager.UploadInput{
		Bucket: aws.String("images-viacom"),
		Key:    aws.String(path),
		Body:   bytes.NewReader(data),
		ACL:    aws.String("public-read"),
	}

	_, err = svc.Upload(input)
	if err != nil {
		fmt.Println("failed to save image to s3:", err)
		pd.log("failed to save image to s3: " + err.Error())
	}
}

func getObjectHelper(id string, namespace string, pd *parametersData) json.RawMessage {
	var o []byte
	var rerr error
	u := strings.Replace(imageIDQuery, "[NAMESPACE]", namespace, 1)
	u = strings.Replace(u, "[KEYID]", id, 1)
	pd.log("Fetching arc call: " + u)
	//check cache to see if we've already made this call
	if pd.cacheRefresh == false {
		cc := redisClient.Get(redisKeyCacheObjectPrefix + u)
		o, _ = cc.Bytes()
	}
	if o == nil {
		//go get the data from arc
		pd.log("fetching data from arc not found in cache")
		resp, err := http.Get(u)

		if err != nil {
			fmt.Println("Error remote url fetch for object by url: ", u, err)
			pd.log("Error remote url fetch for object by url: " + err.Error())
			return nil
		}
		defer resp.Body.Close()

		o, rerr = ioutil.ReadAll(resp.Body)
		if rerr != nil {
			fmt.Println("Error while fetching query", u, rerr)
			pd.log("Error while fetching query: " + rerr.Error())
			return nil
		}

		//save in cache
		redisClient.Set(redisKeyCacheObjectPrefix+u, o, objectCacheTimeout)
	}

	var data responseWrapper

	if err := json.Unmarshal(o, &data); err != nil {
		panic(err)
	}

	if len(data.response.docs) != 1 {
		fmt.Println("Failed to fetch object by id:", id, " url: ", u)
		return nil
	}
	return data.response.docs[0]
}

//getBestImageByMgidID
//returns id, crop width, crop height, offset x, offset y
func getBestImageByMgidID(id string, pd *parametersData) (string, uint, uint, int, int) {
	var item item
	var imgs []image
	var bestImg image
	var bestCropSet virtualImageParams
	var bestCropSetWidth float64
	var bestCropSetHeight float64
	var bestImgInitted = false
	var bestImgRatio float64
	var ratio float64

	mgidPieces := strings.Split(id, ":")

	//example mgid
	//mgid:arc:video:<namespace>:<uuid>
	//TODO: allow for other handlers besides arc
	if len(mgidPieces) < 5 {
		//invalid mgid, mgids must be 5 pieces
	}

	if mgidPieces[1] != "arc" {
		fmt.Println("invalid provider we only support arc currently")
		pd.log("invalid provider we only support arc currently")
		return "", 0, 0, 0, 0
	}
	raw := getObjectHelper(mgidPieces[4], mgidPieces[3], pd)

	json.Unmarshal(raw, &item)

	if len(item.imageAssetRefs) > 0 {
		//image object
		json.Unmarshal(raw, &bestImg)
		imgs = append(imgs, bestImg)
	} else {
		//item object
		//check imagewithcaptions first then images
		for i := 0; i < len(item.imagesWithCaptions); i++ {
			imgs = append(imgs, item.imagesWithCaptions[i].image)
		}
		for i := 0; i < len(item.images); i++ {
			imgs = append(imgs, item.images[i])
		}
	}

	if len(imgs) == 0 {
		return "", 0, 0, 0, 0
	}

	if pd.cw == 0 || pd.ch == 0 {
		//if neither width or height are provided then grab first image and live with it
		return imgs[0].imageAssetRefs[0].uri, 0, 0, 0, 0
	}

	ratio = math.Floor((float64(pd.cw) / float64(pd.ch)) * 10)

	//now run back through them all and find the image that best fits the requested width and height
	//if only width or height are specified grab first image that is greater than the provided values
	pd.log("finding best image...")
	for i := 0; i < len(imgs); i++ {
		pd.log("img index: " + intToString(i))
		if bestImgInitted == false {
			pd.log("always pick the first image by default")
			bestImg = imgs[i]
			bestImgRatio = math.Floor((float64(bestImg.imageAssetRefs[0].width) / float64(bestImg.imageAssetRefs[0].height)) * 10)
			bestImgInitted = true

			if len(imgs[i].virtualImageParams) > 0 {
				bestCropSet = imgs[i].virtualImageParams[0]
				pd.log("best crop called-init: ch=" + uintToString(bestCropSet.cropSizeHeight) + ", cw=" + uintToString(bestCropSet.cropSizeWeight) + ", x=" + intToString(bestCropSet.topLeftX) + ", y=" + intToString(bestCropSet.topLeftY))
				bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
				bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
				bestImgRatio = math.Floor((float64(bestImg.imageAssetRefs[0].width) / float64(bestImg.imageAssetRefs[0].height)) * 10)

				for j := 0; j < len(bestImg.virtualImageParams); j++ {
					newRatio := math.Floor((float64(imgs[i].virtualImageParams[j].cropSizeWeight) / float64(imgs[i].virtualImageParams[j].cropSizeHeight)) * 10)
					diffwidth := math.Abs(float64(pd.cw - imgs[i].virtualImageParams[j].cropSizeWeight))
					diffHeight := math.Abs(float64(pd.ch - imgs[i].virtualImageParams[j].cropSizeHeight))
					pd.log("original ratio: " + floatToString(ratio) + ", new ratio: " + floatToString(newRatio))
					if newRatio == ratio && (diffwidth < bestCropSetWidth || diffHeight < bestCropSetHeight) {
						pd.log("picked cropset processing option 1-init")
						bestCropSet = imgs[i].virtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
						bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].virtualImageParams[j].cropSizeWeight) / float64(imgs[i].virtualImageParams[j].cropSizeHeight)) * 10)
					} else if newRatio == ratio && bestImgRatio != ratio {
						pd.log("picked cropset processing option 2-init: newRatio=" + floatToString(newRatio) + ", bestImgRatio=" + floatToString(bestImgRatio) + ", ratio=" + floatToString(ratio))
						bestCropSet = imgs[i].virtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
						bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].virtualImageParams[j].cropSizeWeight) / float64(imgs[i].virtualImageParams[j].cropSizeHeight)) * 10)
					} else if bestImgRatio != ratio && diffwidth < bestCropSetWidth && diffHeight < bestCropSetHeight {
						pd.log("picked cropset processing option 3-init")
						bestCropSet = imgs[i].virtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
						bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].virtualImageParams[j].cropSizeWeight) / float64(imgs[i].virtualImageParams[j].cropSizeHeight)) * 10)
					}
				}
			}
		} else {
			if len(imgs[i].virtualImageParams) > 0 {
				bestCropSet = imgs[i].virtualImageParams[0]
				pd.log("best crop called: ch=" + uintToString(bestCropSet.cropSizeHeight) + ", cw=" + uintToString(bestCropSet.cropSizeWeight) + ", x=" + intToString(bestCropSet.topLeftX) + ", y=" + intToString(bestCropSet.topLeftY))
				bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
				bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
				bestImgRatio = math.Floor((float64(bestImg.imageAssetRefs[0].width) / float64(bestImg.imageAssetRefs[0].height)) * 10)

				for j := 0; j < len(bestImg.virtualImageParams); j++ {
					newRatio := math.Floor((float64(imgs[i].virtualImageParams[j].cropSizeWeight) / float64(imgs[i].virtualImageParams[j].cropSizeHeight)) * 10)
					diffwidth := math.Abs(float64(pd.cw - imgs[i].virtualImageParams[j].cropSizeWeight))
					diffHeight := math.Abs(float64(pd.ch - imgs[i].virtualImageParams[j].cropSizeHeight))
					pd.log("original ratio: " + floatToString(ratio) + ", new ratio: " + floatToString(newRatio))
					if newRatio == ratio && (diffwidth < bestCropSetWidth || diffHeight < bestCropSetHeight) {
						pd.log("picked cropset processing option 1")
						bestCropSet = imgs[i].virtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
						bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].virtualImageParams[j].cropSizeWeight) / float64(imgs[i].virtualImageParams[j].cropSizeHeight)) * 10)
					} else if newRatio == ratio && bestImgRatio != ratio {
						pd.log("picked cropset processing option 2: newRatio=" + floatToString(newRatio) + ", bestImgRatio=" + floatToString(bestImgRatio) + ", ratio=" + floatToString(ratio))
						bestCropSet = imgs[i].virtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
						bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].virtualImageParams[j].cropSizeWeight) / float64(imgs[i].virtualImageParams[j].cropSizeHeight)) * 10)
					} else if bestImgRatio != ratio && diffwidth < bestCropSetWidth && diffHeight < bestCropSetHeight {
						pd.log("picked cropset processing option 3")
						bestCropSet = imgs[i].virtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
						bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].virtualImageParams[j].cropSizeWeight) / float64(imgs[i].virtualImageParams[j].cropSizeHeight)) * 10)
					}
				}
			} else {
				//image has no crop sets
				newRatio := math.Floor((float64(imgs[i].imageAssetRefs[0].width) / float64(imgs[i].imageAssetRefs[0].height)) * 10)
				diffwidth := math.Abs(float64(pd.cw - imgs[i].imageAssetRefs[0].width))
				diffHeight := math.Abs(float64(pd.ch - imgs[i].imageAssetRefs[0].height))
				pd.log("Image has no crop sets newRatio=" + floatToString(newRatio) + ", ratio=" + floatToString(ratio) + ", diffWidth=" + floatToString(diffwidth) + ", bestCropSetWidth=" + floatToString(bestCropSetWidth) + ", diffHeight=" + floatToString(diffHeight) + ", bestCropSetHeight=" + floatToString(bestCropSetHeight))
				pd.log("checking image asset ref details to check ratio")
				if newRatio == ratio && (diffwidth < bestCropSetWidth || diffHeight < bestCropSetHeight) {
					pd.log("processing b-1")
					bestImg = imgs[i]
					bestCropSetWidth = math.Abs(float64(pd.cw - bestImg.imageAssetRefs[0].width))
					bestCropSetHeight = math.Abs(float64(pd.ch - bestImg.imageAssetRefs[0].height))
					bestImgRatio = math.Floor((float64(bestImg.imageAssetRefs[0].width) / float64(bestImg.imageAssetRefs[0].height)) * 10)
				} else if newRatio == ratio && bestImgRatio != ratio {
					pd.log("processing option b-2: newRatio=" + floatToString(newRatio) + ", bestImgRatio=" + floatToString(bestImgRatio) + ", ratio=" + floatToString(ratio))
					bestImg = imgs[i]
					bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
					bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
					bestImgRatio = math.Floor((float64(bestImg.imageAssetRefs[0].width) / float64(bestImg.imageAssetRefs[0].height)) * 10)
				} else if bestImgRatio != ratio && diffwidth < bestCropSetWidth && diffHeight < bestCropSetHeight {
					pd.log("processing b-3")
					bestImg = imgs[i]
					bestCropSetWidth = math.Abs(float64(pd.cw - bestCropSet.cropSizeWeight))
					bestCropSetHeight = math.Abs(float64(pd.ch - bestCropSet.cropSizeHeight))
					bestImgRatio = math.Floor((float64(bestImg.imageAssetRefs[0].width) / float64(bestImg.imageAssetRefs[0].height)) * 10)
				}
			}
		}
	}

	pd.log("By Id best img uri found was: " + bestImg.imageAssetRefs[0].uri)
	if bestCropSet.cropSizeWeight > 0 {
		pd.log("Best virtual cropset found w:" + uintToString(bestCropSet.cropSizeWeight) + ", h:" + uintToString(bestCropSet.cropSizeHeight) + ", x:" + intToString(bestCropSet.topLeftX) + ", y:" + intToString(bestCropSet.topLeftY))
		return bestImg.imageAssetRefs[0].uri, bestCropSet.cropSizeWeight, bestCropSet.cropSizeHeight, bestCropSet.topLeftX, bestCropSet.topLeftY
	}
	return bestImg.imageAssetRefs[0].uri, 0, 0, 0, 0
}

func generateImage(pd *parametersData, ah string, po string, idflag bool) ([]byte, string) {
	//remove the original prefix of the path which is always 5 characters as it's uri/
	//path to image
	var p string
	//mgid
	var id string
	//crop width
	var cw uint
	//crop height
	var ch uint
	//crop offset x
	var cx int
	//crop offset y
	var cy int
	//full request path
	var fp string

	switch strings.Index(po, "mgid:") {
	default:
		pd.log("Params found for path: " + po)
		//has parameters
		mi := strings.Index(po, "/")
		nv := po[:mi]
		id = po[mi+1:]
		//parse params
		pd.log("index: " + intToString(mi) + ", mgid: " + id + ", params: " + nv)
		findParams(nv, id, pd)
	case -1:
		pd.log("404: Invalid image request")
		return nil, ""
	case 0:
		pd.log("No params found for path: " + po)
		id = po
	}

	//init image magic wand (sets up new image conversion)
	mw := imagick.NewMagickWand()

	//handle logic for fetching image by item id or by image mgid
	//TODO: refactor me
	if idflag == true {
		id, cw, ch, cx, cy = getBestImageByMgidID(id, pd)
		p = strings.Replace(id, ":", "_", -1)
		if cw > 0 && ch > 0 {
			pd.cw = cw
			pd.ch = ch
			pd.cx = cx
			pd.cy = cy
		}

		if id == "" || p == "" {
			//no image found so return missing image
			loadMissingImage(mw, pd)
			fp = imgBaseDir + imageNotFoundPath
		} else {
			fp = imgBaseDir + p
		}
		pd.log("File Path: " + fp)
	} else {
		p = strings.Replace(id, ":", "_", -1)

		if p == "" {
			pd.log("Invalid id requested: " + id)
			return nil, ""
		}

		fp = imgBaseDir + p
		pd.log("File Path: " + fp)
	}

	if pd.cacheRefresh {
		crerr := os.Remove(fp)
		if crerr != nil {
			pd.log("error deleting local cached image: " + crerr.Error())
		}
	}
	//check if the file exists
	i, err := ioutil.ReadFile(fp)
	if err != nil {
		//file not found locally fetch remote
		//see if any other process is fetching the image.  if so then return 404 for now.
		if pd.cacheRefresh || setImageFetchLock(fp, pd) {
			fetchRemoteImageURL(id, p, pd, mw)
		} else {
			loadMissingImage(mw, pd)
			fp = imgBaseDir + imageNotFoundPath
		}
	}
	if err == nil {
		pd.log("Found image locally: " + fp)
		mw.ReadImageBlob(i)
	}

	//get image format/extension and set it for mw
	pd.f = getImageFormat(fp, pd.f, ah, mw.GetImageAlphaChannel(), pd)
	mw.GetImageAlphaChannel()
	pd.log("Extension is:" + pd.f + ", for path: " + fp)
	mw.SetImageFormat(pd.f)
	mw.SetFormat(pd.f)

	//CoalesceImages to break image into layers.  Must be called before any image layer specific operations.
	// aw := mw.CoalesceImages()
	// mw.Destroy()
	fmt.Println(mw.IdentifyImage())

	//Handle Crop
	if pd.cw > 0 && pd.ch > 0 {
		for i := 0; i < int(mw.GetNumberImages()); i++ {
			mw.SetIteratorIndex(i)
			x := pd.cx
			y := pd.cy
			if pd.cc {
				//calculate the x and y for the offset
				// need to fix issue with trying to do math on uint values and how to cast to int from uint
				x = (int(mw.GetImageWidth()) - int(pd.cw)) / 2
				y = (int(mw.GetImageHeight()) - int(pd.ch)) / 2
			}
			pd.log("Crop image: " + fp + " x=" + intToString(x) + ", y=" + intToString(y))
			mw.CropImage(pd.cw, pd.ch, x, y)
			mw.SetImagePage(pd.cw, pd.ch, 0, 0)
		}
	}

	//Handle Resize
	if pd.rw > 0 || pd.rh > 0 {
		for i := 0; i < int(mw.GetNumberImages()); i++ {
			mw.SetIteratorIndex(i)
			x := mw.GetImageWidth()
			y := mw.GetImageHeight()
			ratio := uint((float64(x) / float64(y)) * 100)
			if pd.rw > 0 && pd.rh == 0 {
				x = pd.rw
				y = (pd.rw * ratio) / 100
			}
			if pd.rh > 0 && pd.rw == 0 {
				x = (pd.rh * ratio / 100)
				y = pd.rh
			}
			tierr := mw.ThumbnailImage(x, y)
			if tierr != nil {
				pd.log("Failed to create thumbnail image: " + tierr.Error())
			}
			mw.SetImagePage(pd.rw, pd.rh, 0, 0)
		}
	}

	//DeconstructImages after all resize and other image layer specifc operations
	// mw = aw.DeconstructImages()
	// aw.Destroy()

	//Handle Normalize
	if pd.n {
		mw.NormalizeImage()
	}

	mw.SetOption("png:compression-level", "9")
	mw.SetOption("png:compression-filter", "5")
	mw.SetOption("png:compression-strategy", "1")
	mw.SetOption("jpeg:fancy-upsampling", "off")
	mw.SetOption("filter:support", "2")
	mw.SetOption("png:exclude-chunk", "all")
	mw.SetColorspace(imagick.COLORSPACE_SRGB)
	// mw.SetInterlaceScheme(imagick.INTERLACE_NO)
	// mw.SharpenImage(0.25, 0.25)
	// mw.PosterizeImage(136, false)

	//Handle Quality
	if pd.q > 0 {
		switch pd.f {
		case "png":
			// the image format is a pain so please don't use it if possible
			mw.SetImageCompression(imagick.COMPRESSION_LOSSLESS_JPEG)
			mw.SetImageCompressionQuality(pd.q)
		case "webp":
			mw.SetImageCompression(imagick.COMPRESSION_LOSSLESS_JPEG)
			mw.SetImageCompressionQuality(pd.q)
		case "jpg":
			mw.SetImageCompression(imagick.COMPRESSION_JPEG)
			mw.SetImageCompressionQuality(pd.q)
		}
	}

	mw.StripImage()

	ib := mw.GetImageBlob()
	mw.Destroy()
	return ib, pd.f
}

func outputDebug(w http.ResponseWriter, pd *parametersData) {
	w.Header().Set("Content-Type", "text/html")
	t := "<html><body><h1>Debug Output:</h1>"
	for _, v := range pd.msgs {
		t += "<li>" + v + "</li>"
	}
	t += "</body></html>"
	fmt.Fprint(w, t)
}

func handlerImageURI(w http.ResponseWriter, r *http.Request) {
	//params init
	var pd parametersData
	var i []byte
	var err error
	var f string
	var cc *redis.StringCmd
	pd.cc = false
	pd.n = false

	qs := r.URL.Query()

	if _, ok := qs["help"]; ok {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintln(w, helpMsg)
		return
	}

	_, pd.cacheRefresh = qs["cacheRefresh"]
	_, pd.debug = qs["debug"]

	//check to see if the image is in redis cache
	if pd.cacheRefresh == false {
		cc = redisClient.Get(redisKeyCachePrefix + r.URL.Path)
		i, err = cc.Bytes()
	}

	if i != nil {
		pd.log("Image cache found for: " + r.URL.Path)
		cc = redisClient.Get(redisKeyCacheFormatPrefix + r.URL.Path)
		if pd.debug {
			pd.log("Expires in: " + redisClient.TTL(redisKeyCacheFormatPrefix+r.URL.Path).Val().String())
		}
		f = cc.Val()
		if f == "" {
			pd.log("Error while retrieving cache data for redis: " + err.Error())
			//setting i to nil to force the image generation because we didn't get a format for the image cache
			i = nil
		}
	}

	//if not then create the image
	if i == nil {
		pd.log("Generating image for " + r.URL.Path)
		//remove the original prefix of the path which is always 5 characters as it's uri/
		i, f = generateImage(&pd, r.Header.Get("Accept"), r.URL.Path[5:], false)
		//add to redis cache
		scf := redisClient.Set(redisKeyCacheFormatPrefix+r.URL.Path, f, imageCacheTimeout)
		if scf.Err() != nil {
			//failed to save the image cache to redis
			pd.log("Failed to save an image format to redis cache: " + r.URL.Path + ", Error: " + scf.Err().Error())
			//skipping error as we can still survive
		}

		if scf.Err() == nil {
			scc := redisClient.Set(redisKeyCachePrefix+r.URL.Path, i, imageCacheTimeout)
			if scc.Err() != nil {
				//failed to save the image cache to redis
				pd.log("Failed to save an image to redis cache: " + r.URL.Path + ", Error: " + scc.Err().Error())
				//skipping error as we can still survive
			}
		}
	}

	//everything failed check
	if i == nil {
		//need to have a better error handling here, at least set 404 but this should only occur when the default img is not working
		return
	}

	if pd.debug {
		outputDebug(w, &pd)
		return
	}
	w.Header().Set("Content-Type", "image/"+f)
	w.Write(i)
}

func handlerImageID(w http.ResponseWriter, r *http.Request) {
	//params init
	var pd parametersData
	var i []byte
	var err error
	var f string
	var cc *redis.StringCmd
	pd.cc = false
	pd.n = false

	qs := r.URL.Query()

	if _, ok := qs["help"]; ok {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintln(w, helpMsg)
		return
	}

	_, pd.cacheRefresh = qs["cacheRefresh"]
	_, pd.debug = qs["debug"]

	//check to see if the image is in redis cache
	if pd.cacheRefresh == false {
		cc = redisClient.Get(redisKeyCachePrefix + r.URL.Path)
		i, err = cc.Bytes()
	}

	if i != nil {
		pd.log("Image cache found for " + r.URL.Path)
		cc = redisClient.Get(redisKeyCacheFormatPrefix + r.URL.Path)
		f = cc.Val()
		if f == "" {
			pd.log("Error while retrieving cache data for redis: " + err.Error())
			//setting i to nil to force the image generation because we didn't get a format for the image cache
			i = nil
		}
	}

	//if not then create the image
	if i == nil {
		pd.log("Fetching image information")

		pd.log("Generating image for " + r.URL.Path)
		//remove the original prefix of the path which is always 5 characters as it's uri/
		i, f = generateImage(&pd, r.Header.Get("Accept"), r.URL.Path[5:], true)
		//add to redis cache
		scf := redisClient.Set(redisKeyCacheFormatPrefix+r.URL.Path, f, imageCacheTimeout)
		if scf.Err() != nil {
			//failed to save the image cache to redis
			fmt.Println("Failed to save an image format to redis cache", r.URL.Path, scf.Err())
			pd.log("Failed to save an image format to redis cache: " + r.URL.Path + ", Error: " + scf.Err().Error())
			//skipping error as we can still survive
		}

		if scf.Err() == nil {
			scc := redisClient.Set(redisKeyCachePrefix+r.URL.Path, i, imageCacheTimeout)
			if scc.Err() != nil {
				//failed to save the image cache to redis
				fmt.Println("Failed to save an image to redis cache", r.URL.Path, scc.Err())
				//skipping error as we can still survive
			}
		}
	}

	//everything failed check
	if i == nil {
		//need to have a better error handling here, at least set 404 but this should only occur when the default img is not working
		pd.log("No image data found due to missing default image")
		if pd.debug == false {
			return
		}
	}

	if pd.debug {
		outputDebug(w, &pd)
		return
	}

	w.Header().Set("Content-Type", "image/"+f)
	w.Write(i)
}

func main() {
	remoteImgURL = os.Getenv("REMOTE_IMG_URL")
	if remoteImgURL == "" {
		fmt.Println("Missing environment variable REMOTE_IMG_URL which should point to the remote base url to pass the requested paths onto")
		os.Exit(1)
	}
	imgBaseDir = os.Getenv("IMG_PATH")
	if imgBaseDir == "" {
		fmt.Println("Missing enviroment variable IMG_PATH which should point to the folder path where your local images will be stored")
		os.Exit(1)
	}
	imageNotFoundPath = os.Getenv("DEFAULT_IMG")
	if imageNotFoundPath == "" {
		fmt.Println("Missing environment variable DEFAULT_IMG which should be the path to the default image in the IMG_PATH")
		os.Exit(1)
	}

	imgIDDomain := os.Getenv("IMG_ID_URL")
	if imgIDDomain == "" {
		fmt.Println("Missing environment variable IMG_ID_URL which should be the path to the image data object server with a slash on the end")
		os.Exit(1)
	}
	imageIDQuery = imgIDDomain + imageIDQueryString

	imagick.Initialize()
	//defer imagick.Terminate()

	redisADDR = os.Getenv("REDIS_PORT_6379_TCP_ADDR")
	if redisADDR == "" {
		redisADDR = "localhost"
	}

	redisClient = redis.NewClient(&redis.Options{
		Addr:     redisADDR + ":6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	fmt.Println("Image Server Ready")

	http.HandleFunc("/uri/", handlerImageURI)
	http.HandleFunc("/oid/", handlerImageID)
	http.HandleFunc("/", handlerHelp)
	http.ListenAndServe(":8080", nil)
}

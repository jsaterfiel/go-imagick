// Image Server
// Requirements:
// - Imagemagick libs installed on machine with latest version of 6 I have ImageMagick 6.9.8-10 Q16
// - Redis server running locally with the standard ports on the latest version
//
// You will want to install these Go packages:
// go get gopkg.in/gographics/imagick.v2/imagick  (see: https://github.com/gographics/imagick)
// go get gopkg.in/redis.v4 (see: https://github.com/go-redis/redis)
package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	//"github.com/aws/aws-sdk-go/aws"
	//"github.com/aws/aws-sdk-go/aws/session"
	//"github.com/aws/aws-sdk-go/service/s3"
	"gopkg.in/gographics/imagick.v2/imagick"
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

var imageIdQuery string

var redisADDR string

var redisClient *redis.Client

var cacheRefresh = false

//var s3Client *s3.S3

const imageIdQueryString = "jp/[NAMESPACE]?&q={%22select%22:{%22VirtualImageParams%22:{%22*%22:1},%22ImageAssetRefs%22:{%22Height%22:1,%22Width%22:1,%22URI%22:1},%22ImagesWithCaptions%22:{%22Image%22:{%22VirtualImageParams%22:{%22*%22:1},%22ImageAssetRefs%22:{%22Height%22:1,%22Width%22:1,%22URI%22:1}}},%22VirtualImageParams%22:{%22*%22:1},%22Images%22:{%22VirtualImageParams%22:{%22*%22:1},%22ImageAssetRefs%22:{%22Height%22:1,%22Width%22:1,%22URI%22:1}}},%22vars%22:{},%22where%22:{%22byId%22:[%22[KEYID]%22]},%22start%22:0,%22rows%22:1,%22omitNumFound%22:true,%22debug%22:{}}&stage=authoring&filterSchedules=true&dateFormat=UTC"

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
debug - Returns information about how the requested url was processed. No image is returned. (not implemented yet)
cacheRefresh - clears the cache for this image request and fetches the image from the remote url
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

//ResponseWrapper Arc response wrapper
type ResponseWrapper struct {
	Response struct {
		Docs []json.RawMessage
	}
}

type ImageFormat struct {
	TypeName string
}
type ImageAssetRefs struct {
	Format ImageFormat
	Height uint
	Width  uint
	URI    string
}
type VirtualImageParams struct {
	TopLeftX       int
	TopLeftY       int
	CropSizeWidth  uint
	CropSizeHeight uint
}
type Image struct {
	ImageAssetRefs     []ImageAssetRefs
	VirtualImageParams []VirtualImageParams
}

type Item struct {
	Images             []Image
	ImagesWithCaptions []struct {
		Image Image
	}
	Image
}

func handlerHelp(w http.ResponseWriter, _ *http.Request) {
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
			fmt.Println("Unknown Parameter=", nv[0], "for mgid=", m)
		}
		fmt.Println("Found param: ", nv)
	}
}

// p is the path to the image
func setImageFetchLock(p string) bool {
	k := redisKeyLockPrefix + p
	v := redisClient.SetNX(k, "true", imageFetchTimeout)
	if v.Err() != nil {
		fmt.Println("Unable to set the key: ", k, v.Err())
		panic("Error communicating with Redis")
	}
	return v.Val()
}

// i is image path
// f is the requested format (if any)
// ha is header accept string
func getImageFormat(i string, f string, ha string) string {
	of := i[strings.LastIndex(i, ".")+1:]

	fmt.Println("Extension is:", of, "for path:", i)

	//Handle image format change
	fmt.Println("Header: ", ha)
	if strings.Contains(ha, "image/webp") {
		of = "webp"
		fmt.Println("Browser requested webp for image: ", i)
	}

	return of
}

func loadMissingImage(mw *imagick.MagickWand) {
	fp := imgBaseDir + imageNotFoundPath
	//check if the file exists
	i, err := ioutil.ReadFile(fp)
	if err != nil {
		//file not found locally fetch remote
		//see if any other process is fetching the image.  if so then return 404 for now.
		err = nil
		if setImageFetchLock(fp) == true {
			fetchRemoteImageURL(imageNotFoundPath, imageNotFoundPath, mw)
		} else {
			fmt.Println("Cannot load default image", fp)
			panic("Cannot load default image")
		}
	} else {
		fmt.Println("Found image locally: ", fp)
		mw.ReadImageBlob(i)
	}
}

func fetchRemoteImageURL(m string, p string, mw *imagick.MagickWand) {
	url := remoteImgURL + m + "?q=.9"
	fmt.Println("Remote fetch Image: ", url)
	//try to remotely fetch the image
	resp, err := http.Get(url)

	if err != nil {
		fmt.Println("Error remote url fetch, path=", url)
		loadMissingImage(mw)
		return
	} else {
		defer resp.Body.Close()
	}

	i, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Failed to fetch remote image: ", url, err)
		loadMissingImage(mw)
		return
	}

	fmt.Println("Fetched Remote Image: ", url)

	//get image folder path
	ifi := strings.LastIndex(p, "/")
	ifp := imgBaseDir + "/" + p[:ifi]

	fmt.Println("Creating Directories: ", ifp)

	//write out the image to file for future usage
	err = os.MkdirAll(ifp, 0777)
	if err != nil {
		fmt.Println("Failed to create new directories for path: ", ifp, err)
		panic(err)
	}

	ip := imgBaseDir + "/" + p
	f, err := os.Create(ip)

	if err != nil {
		fmt.Println("Failed to create file: ", ip, err)
		panic(err)
	} else {
		defer f.Close()
	}

	ib, err := f.Write(i)
	fmt.Println("Bytes written to file: ", ip)
	if ib < 1 && err != nil {
		fmt.Println("Failed to write to file: ", ip)
		if err != nil {
			panic(err)
		}
	}

	mw.ReadImageBlob(i)
}

func getObjectHelper(id string, namespace string) json.RawMessage {
	var o []byte
	var rerr error
	u := strings.Replace(imageIdQuery, "[NAMESPACE]", namespace, 1)
	u = strings.Replace(u, "[KEYID]", id, 1)
	fmt.Println("Fetching arc call: " + u)
	//check cache to see if we've already made this call
	if cacheRefresh == false {
		cc := redisClient.Get(redisKeyCacheObjectPrefix + u)
		o, _ = cc.Bytes()
	}
	if o == nil {
		//go get the data from arc
		fmt.Println("fetching data from arc not found in cache")
		resp, err := http.Get(u)

		if err != nil {
			fmt.Println("Error remote url fetch for object by url: ", u)
			return nil
		} else {
			defer resp.Body.Close()
		}

		o, rerr = ioutil.ReadAll(resp.Body)
		if rerr != nil {
			fmt.Println("Error while fetching query", u, rerr)
			return nil
		}

		//save in cache
		redisClient.Set(redisKeyCacheObjectPrefix+u, o, objectCacheTimeout)
	}

	var data ResponseWrapper

	if err := json.Unmarshal(o, &data); err != nil {
		panic(err)
	}

	if len(data.Response.Docs) != 1 {
		fmt.Println("Failed to fetch object by id:", id, " url: ", u)
		return nil
	}
	return data.Response.Docs[0]
}

//getBestImageByMgidId
//returns id, crop width, crop height, offset x, offset y
func getBestImageByMgidId(id string, width uint, height uint) (string, uint, uint, int, int) {
	var item Item
	var imgs []Image
	var bestImg Image
	var bestCropSet VirtualImageParams
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
		return "", 0, 0, 0, 0
	}
	raw := getObjectHelper(mgidPieces[4], mgidPieces[3])

	json.Unmarshal(raw, &item)

	if len(item.ImageAssetRefs) > 0 {
		//image object
		json.Unmarshal(raw, &bestImg)
		imgs = append(imgs, bestImg)
	} else {
		//item object
		//check imagewithcaptions first then images
		for i := 0; i < len(item.ImagesWithCaptions); i++ {
			imgs = append(imgs, item.ImagesWithCaptions[i].Image)
		}
		for i := 0; i < len(item.Images); i++ {
			imgs = append(imgs, item.Images[i])
		}
	}

	if len(imgs) == 0 {
		return "", 0, 0, 0, 0
	}

	if width == 0 || height == 0 {
		//if neither width or height are provided then grab first image and live with it
		return imgs[0].ImageAssetRefs[0].URI, 0, 0, 0, 0
	}

	ratio = math.Floor((float64(width) / float64(height)) * 10)

	//now run back through them all and find the image that best fits the requested width and height
	//if only width or height are specified grab first image that is greater than the provided values
	for i := 0; i < len(imgs); i++ {
		fmt.Println("img", i)
		if bestImgInitted == false {
			//always pick the first image by default
			bestImg = imgs[i]
			bestImgRatio = math.Floor((float64(bestImg.ImageAssetRefs[0].Width) / float64(bestImg.ImageAssetRefs[0].Height)) * 10)
			bestImgInitted = true

			if len(imgs[i].VirtualImageParams) > 0 {
				bestCropSet = imgs[i].VirtualImageParams[0]
				fmt.Println("best crop called", bestCropSet)
				bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
				bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
				bestImgRatio = math.Floor((float64(bestImg.ImageAssetRefs[0].Width) / float64(bestImg.ImageAssetRefs[0].Height)) * 10)

				for j := 0; j < len(bestImg.VirtualImageParams); j++ {
					newRatio := math.Floor((float64(imgs[i].VirtualImageParams[j].CropSizeWidth) / float64(imgs[i].VirtualImageParams[j].CropSizeHeight)) * 10)
					diffWidth := math.Abs(float64(width - imgs[i].VirtualImageParams[j].CropSizeWidth))
					diffHeight := math.Abs(float64(height - imgs[i].VirtualImageParams[j].CropSizeHeight))
					fmt.Println("original ratio", ratio, "new ratio", newRatio)
					if newRatio == ratio && (diffWidth < bestCropSetWidth || diffHeight < bestCropSetHeight) {
						fmt.Println("1")
						bestCropSet = imgs[i].VirtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
						bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].VirtualImageParams[j].CropSizeWidth) / float64(imgs[i].VirtualImageParams[j].CropSizeHeight)) * 10)
					} else if newRatio == ratio && bestImgRatio != ratio {
						fmt.Println("2", newRatio, bestImgRatio, ratio)
						bestCropSet = imgs[i].VirtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
						bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].VirtualImageParams[j].CropSizeWidth) / float64(imgs[i].VirtualImageParams[j].CropSizeHeight)) * 10)
					} else if bestImgRatio != ratio && diffWidth < bestCropSetWidth && diffHeight < bestCropSetHeight {
						fmt.Println("3")
						bestCropSet = imgs[i].VirtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
						bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].VirtualImageParams[j].CropSizeWidth) / float64(imgs[i].VirtualImageParams[j].CropSizeHeight)) * 10)
					}
				}
			}
		} else {
			if len(imgs[i].VirtualImageParams) > 0 {
				bestCropSet = imgs[i].VirtualImageParams[0]
				fmt.Println("best crop called", bestCropSet)
				bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
				bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
				bestImgRatio = math.Floor((float64(bestImg.ImageAssetRefs[0].Width) / float64(bestImg.ImageAssetRefs[0].Height)) * 10)

				for j := 0; j < len(bestImg.VirtualImageParams); j++ {
					newRatio := math.Floor((float64(imgs[i].VirtualImageParams[j].CropSizeWidth) / float64(imgs[i].VirtualImageParams[j].CropSizeHeight)) * 10)
					diffWidth := math.Abs(float64(width - imgs[i].VirtualImageParams[j].CropSizeWidth))
					diffHeight := math.Abs(float64(height - imgs[i].VirtualImageParams[j].CropSizeHeight))
					fmt.Println("original ratio", ratio, "new ratio", newRatio)
					if newRatio == ratio && (diffWidth < bestCropSetWidth || diffHeight < bestCropSetHeight) {
						fmt.Println("1")
						bestCropSet = imgs[i].VirtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
						bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].VirtualImageParams[j].CropSizeWidth) / float64(imgs[i].VirtualImageParams[j].CropSizeHeight)) * 10)
					} else if newRatio == ratio && bestImgRatio != ratio {
						fmt.Println("2", newRatio, bestImgRatio, ratio)
						bestCropSet = imgs[i].VirtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
						bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].VirtualImageParams[j].CropSizeWidth) / float64(imgs[i].VirtualImageParams[j].CropSizeHeight)) * 10)
					} else if bestImgRatio != ratio && diffWidth < bestCropSetWidth && diffHeight < bestCropSetHeight {
						fmt.Println("3")
						bestCropSet = imgs[i].VirtualImageParams[j]
						bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
						bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
						bestImgRatio = math.Floor((float64(imgs[i].VirtualImageParams[j].CropSizeWidth) / float64(imgs[i].VirtualImageParams[j].CropSizeHeight)) * 10)
					}
				}
			} else {
				//image has no crop sets
				newRatio := math.Floor((float64(imgs[i].ImageAssetRefs[0].Width) / float64(imgs[i].ImageAssetRefs[0].Height)) * 10)
				diffWidth := math.Abs(float64(width - imgs[i].ImageAssetRefs[0].Width))
				diffHeight := math.Abs(float64(height - imgs[i].ImageAssetRefs[0].Height))
				fmt.Println("Image has no crop sets r", newRatio, ratio, "w", diffWidth, bestCropSetWidth, "h", diffHeight, bestCropSetHeight)
				if newRatio == ratio && (diffWidth < bestCropSetWidth || diffHeight < bestCropSetHeight) {
					fmt.Println("b-1")
					bestImg = imgs[i]
					bestCropSetWidth = math.Abs(float64(width - bestImg.ImageAssetRefs[0].Width))
					bestCropSetHeight = math.Abs(float64(height - bestImg.ImageAssetRefs[0].Height))
					bestImgRatio = math.Floor((float64(bestImg.ImageAssetRefs[0].Width) / float64(bestImg.ImageAssetRefs[0].Height)) * 10)
				} else if newRatio == ratio && bestImgRatio != ratio {
					fmt.Println("b-2", newRatio, bestImgRatio, ratio)
					bestImg = imgs[i]
					bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
					bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
					bestImgRatio = math.Floor((float64(bestImg.ImageAssetRefs[0].Width) / float64(bestImg.ImageAssetRefs[0].Height)) * 10)
				} else if bestImgRatio != ratio && diffWidth < bestCropSetWidth && diffHeight < bestCropSetHeight {
					fmt.Println("b-3")
					bestImg = imgs[i]
					bestCropSetWidth = math.Abs(float64(width - bestCropSet.CropSizeWidth))
					bestCropSetHeight = math.Abs(float64(height - bestCropSet.CropSizeHeight))
					bestImgRatio = math.Floor((float64(bestImg.ImageAssetRefs[0].Width) / float64(bestImg.ImageAssetRefs[0].Height)) * 10)
				}
			}
		}
	}

	fmt.Println("By Id best img uri found was:", bestImg.ImageAssetRefs[0].URI)
	if bestCropSet.CropSizeWidth > 0 {
		fmt.Println("Best virtual cropset found w:", bestCropSet.CropSizeWidth, "h:", bestCropSet.CropSizeHeight, "x:", bestCropSet.TopLeftX, "y:", bestCropSet.TopLeftY)
		return bestImg.ImageAssetRefs[0].URI, bestCropSet.CropSizeWidth, bestCropSet.CropSizeHeight, bestCropSet.TopLeftX, bestCropSet.TopLeftY
	}
	return bestImg.ImageAssetRefs[0].URI, 0, 0, 0, 0
}

func generateImage(params parametersData, ah string, po string, idflag bool) ([]byte, string) {
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
		fmt.Println("Params found for path: ", po)
		//has parameters
		mi := strings.Index(po, "/")
		nv := po[:mi]
		id = po[mi+1:]
		//parse params
		fmt.Println("index=", mi, "mgid=", id, "params=", mi, id, nv)
		findParams(nv, id, &params)
	case -1:
		fmt.Println("404: Invalid image request")
		return nil, ""
	case 0:
		fmt.Println("No params found for path: ", po)
		id = po
	}

	//init image magic wand (sets up new image conversion)
	mw := imagick.NewMagickWand()

	//handle logic for fetching image by item id or by image mgid
	//TODO: refactor me
	if idflag == true {
		id, cw, ch, cx, cy = getBestImageByMgidId(id, params.rw, params.rh)
		p = strings.Replace(id, ":", "_", -1)
		if cw > 0 && ch > 0 {
			params.cw = cw
			params.ch = ch
			params.cx = cx
			params.cy = cy
		}

		if id == "" || p == "" {
			//no image found so return missing image
			loadMissingImage(mw)
			fp = imgBaseDir + imageNotFoundPath
		} else {
			fp = imgBaseDir + p
		}

		fmt.Println("File Path:", fp)

	} else {
		p = strings.Replace(id, ":", "_", -1)

		if p == "" {
			fmt.Println("Invalid id requested: ", id)
			return nil, ""
		}

		fp = imgBaseDir + p
		fmt.Println("File Path:", fp)
	}

	if cacheRefresh {
		crerr := os.Remove(fp)
		if crerr != nil {
			fmt.Println("error deleting local cached image", crerr)
		}
	}
	//check if the file exists
	i, err := ioutil.ReadFile(fp)
	if err != nil {
		//file not found locally fetch remote
		//see if any other process is fetching the image.  if so then return 404 for now.
		if setImageFetchLock(fp) == true {
			fetchRemoteImageURL(id, p, mw)
		} else {
			loadMissingImage(mw)
			fp = imgBaseDir + imageNotFoundPath
		}
	}
	if err == nil {
		fmt.Println("Found image locally: ", fp)
		mw.ReadImageBlob(i)
	}

	//get image format/extension and set it for mw
	params.f = getImageFormat(fp, p, ah)
	fmt.Println("Extension is:", params.f, "for path:", fp)
	mw.SetImageFormat(params.f)
	mw.SetFormat(params.f)

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
			fmt.Println("Crop image: ", fp, " x=", x, ", y=", y)
			mw.CropImage(params.cw, params.ch, x, y)
			mw.SetImagePage(params.cw, params.ch, 0, 0)
		}
	}

	//Handle Resize
	if params.rw > 0 || params.rh > 0 {
		fmt.Println("Resizing image:", id, ", width:", params.rw, "height:", params.rh)
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

	//Handle Quality
	if params.q > 0 {
		switch params.f {
		case "png":
		case "webp":
			mw.SetImageCompression(imagick.COMPRESSION_LOSSLESS_JPEG)
			break
		case "jpg":
		case "jpeg":
		default:
			mw.SetImageCompression(imagick.COMPRESSION_JPEG)
			break
		}
		mw.SetImageCompressionQuality(params.q)
	}

	mw.StripImage()

	return mw.GetImageBlob(), params.f
}

func handlerImageURI(w http.ResponseWriter, r *http.Request) {
	//params init
	var params parametersData
	var i []byte
	var err error
	var f string
	var cc *redis.StringCmd
	params.cc = false
	params.n = false

	qs := r.URL.Query()

	if _, ok := qs["help"]; ok {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintln(w, helpMsg)
		return
	}

	_, cacheRefresh = qs["cacheRefresh"]

	//check to see if the image is in redis cache
	if cacheRefresh == false {
		cc = redisClient.Get(redisKeyCachePrefix + r.URL.Path)
		i, err = cc.Bytes()
	}

	if i != nil {
		fmt.Println("Image cache found for", r.URL.Path)
		cc = redisClient.Get(redisKeyCacheFormatPrefix + r.URL.Path)
		f = cc.Val()
		if f == "" {
			fmt.Println("Error while retrieving cache data for redis", err)
			//setting i to nil to force the image generation because we didn't get a format for the image cache
			i = nil
		}
	}

	//if not then create the image
	if i == nil {
		fmt.Println("Generating image for", r.URL.Path)
		//remove the original prefix of the path which is always 5 characters as it's uri/
		i, f = generateImage(params, r.Header.Get("Accept"), r.URL.Path[5:], false)
		//add to redis cache
		scf := redisClient.Set(redisKeyCacheFormatPrefix+r.URL.Path, f, imageCacheTimeout)
		if scf.Err() != nil {
			//failed to save the image cache to redis
			fmt.Println("Failed to save an image format to redis cache", r.URL.Path, scf.Err())
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
		return
	}

	w.Header().Set("Content-Type", "image/"+f)
	w.Write(i)
}

func handlerImageId(w http.ResponseWriter, r *http.Request) {
	//params init
	var params parametersData
	var i []byte
	var err error
	var f string
	var cc *redis.StringCmd
	params.cc = false
	params.n = false

	qs := r.URL.Query()

	if _, ok := qs["help"]; ok {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintln(w, helpMsg)
		return
	}

	_, cacheRefresh = qs["cacheRefresh"]

	//check to see if the image is in redis cache
	if cacheRefresh == false {
		cc = redisClient.Get(redisKeyCachePrefix + r.URL.Path)
		i, err = cc.Bytes()
	}

	if i != nil {
		fmt.Println("Image cache found for", r.URL.Path)
		cc = redisClient.Get(redisKeyCacheFormatPrefix + r.URL.Path)
		f = cc.Val()
		if f == "" {
			fmt.Println("Error while retrieving cache data for redis", err)
			//setting i to nil to force the image generation because we didn't get a format for the image cache
			i = nil
		}
	}

	//if not then create the image
	if i == nil {
		fmt.Println("Fetching image information")

		fmt.Println("Generating image for", r.URL.Path)
		//remove the original prefix of the path which is always 5 characters as it's uri/
		i, f = generateImage(params, r.Header.Get("Accept"), r.URL.Path[5:], true)
		//add to redis cache
		scf := redisClient.Set(redisKeyCacheFormatPrefix+r.URL.Path, f, imageCacheTimeout)
		if scf.Err() != nil {
			//failed to save the image cache to redis
			fmt.Println("Failed to save an image format to redis cache", r.URL.Path, scf.Err())
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

	imgIdDomain := os.Getenv("IMG_ID_URL")
	if imgIdDomain == "" {
		fmt.Println("Missing environment variable IMG_ID_URL which should be the path to the image data object server with a slash on the end")
		os.Exit(1)
	}
	imageIdQuery = imgIdDomain + imageIdQueryString

	imagick.Initialize()
	defer imagick.Terminate()

	redisADDR = os.Getenv("REDIS_PORT_6379_TCP_ADDR")
	if redisADDR == "" {
		redisADDR = "localhost"
	}

	redisClient = redis.NewClient(&redis.Options{
		Addr:     redisADDR + ":6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	pong, err := redisClient.Ping().Result()
	fmt.Println("Redis server ping result:", pong, err)

	fmt.Println("Image Server Ready")

	//sess, err := session.NewSession(&aws.Config{
	//	Region: aws.String("us-east-1")},
	//)

	// Create S3 service client
	//s3Client := s3.New(sess)
	//
	//resp, err := s3Client.ListObjects(&s3.ListObjectsInput{Bucket: aws.String("images-viacom")})
	//
	//if err != nil {
	//	fmt.Println("Unable to list items in bucket", err)
	//}
	//
	//for _, item := range resp.Contents {
	//	fmt.Println("Name:         ", *item.Key)
	//	fmt.Println("Last modified:", *item.LastModified)
	//	fmt.Println("Size:         ", *item.Size)
	//	fmt.Println("Storage class:", *item.StorageClass)
	//	fmt.Println("")
	//}

	http.HandleFunc("/uri/", handlerImageURI)
	http.HandleFunc("/oid/", handlerImageId)
	http.HandleFunc("/", handlerHelp)
	http.ListenAndServe(":8080", nil)
}

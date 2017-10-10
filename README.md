# go-imagick image server
An implementation of ImageMagick in go providing a basic image server implementation.

The code will attempt to fetch the provided mgid string from the remote image location and save the image locally to speed up future requests.

Redis is used across instances of this server to ensure only one server will attempt to fetch and process an image.

At the moment the code is very specific to how images work which most people probably don't use.  I will add a solution that doesn't rely upon mgid patterns for everybody else to be able to use this image server soon.

Tests will be coming soon as well.

Will also upgrade the code to use imagemagick 7+ with gopkg.in/gographics/imagick.v3/imagick soon

# Requirements
* Imagemagick libs installed on machine with latest version of 6 I have ImageMagick 6.9.8-10 Q16 (see: https://www.imagemagick.org/script/index.php)
* Redis server running locally with the standard ports on the latest version

You will want to install these Go packages:
* go get gopkg.in/gographics/imagick.v3/imagick  (see: https://github.com/gographics/imagick)
* go get gopkg.in/redis.v4 (see: https://github.com/go-redis/redis)

Environment Variables used by GO and are all required:
* IMG_PATH - the full path to the directory that will hold your images with slash on the end
* DEFAULT_IMG - the relative path in the IMG_PATH for the 404 page.  No slash at beginning of the path.
* REMOTE_IMG_URL - the remote image location to get the original image from
* IMG_ID_URL - the object id fetch system being called with slash at the end

# Features
* Resize image
* Crop Image
* Adjust quality levels (by default all images are pulled with a 90% compression from the original image servers if not on the local volume)
* Supports jpeg, png, gif, animated gif, webp
* Special helpers for animated gif
 * Still image - the first frame of the animated gif.  Great for creating a placeholder then loading the animated gif later to cut down on bandwidth during initial page loads
 * Preview mode - reduces the frames of the animated gif to 5 and add a 1.5 second time between them.  Great if you need a wall of animated gif previews as it'll cut down on the sizes.

#Docker Image
Docker file has been including for building the docker image.  You will need to pass in the environment variables to the container when running it.


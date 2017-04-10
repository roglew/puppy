The Puppy Proxy (New Pappy)
===========================

For documentation on what the commands are, see the [Pappy README](https://github.com/roglew/pappy-proxy)

What is this?
-------------
This is a beta version of what I plan on releasing as the next version of Pappy. Technically it should work, but there are a few missing features that I want to finish before replacing Pappy. A huge part of the code has been rewritten in Go and most commands have been reimplemented.

**Back up your data.db files before using this**. The database schema may change and I may or may not correctly upgrade it from the published schema version here. It also breaks backwards compatibility with the last version of Pappy.

Installation
------------

1. [Set up go](https://golang.org/doc/install)
1. [Set up pip](https://pip.pypa.io/en/stable/)

Then run:

~~~
# Get puppy and all its dependencies
go get https://github.com/roglew/puppy
cd ~/$GOPATH/puppy
go get ./...

# Build the go binary
cd ~/$GOPATH/bin
go build puppy
cd ~/$GOPATH/src/puppy/python/puppy

# Optionally set up the virtualenv here

# Set up the python interface
pip install -e .
~~~

Then you can run puppy by running `puppy`. It will use the puppy binary in `~/$GOPATH/bin` so leave the binary there.

Missing Features From Pappy
---------------------------
Here's what Pappy can do that this can't:

- The `http://pappy` interface
- Upstream proxies
- Commands taking multiple requests
- Any and all documentation
- The macro API is totally different

Need more info?
---------------
Right now I haven't written any documentation, so feel free to contact me for help.
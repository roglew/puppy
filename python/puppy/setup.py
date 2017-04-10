#!/usr/bin/env python

import pkgutil
#import pappyproxy
from setuptools import setup, find_packages

VERSION = "0.0.1"

setup(name='puppyproxy',
      version=VERSION,
      description='The Puppy Intercepting Proxy',
      author='Rob Glew',
      author_email='rglew56@gmail.com',
      #url='https://www.github.com/roglew/puppy-proxy',
      packages=['puppyproxy'],
      include_package_data = True,
      license='MIT',
      entry_points = {
          'console_scripts':['puppy = puppyproxy.pup:start'],
          },
      #long_description=open('docs/source/overview.rst').read(),
      long_description="The Puppy Proxy",
      keywords='http proxy hacking 1337hax pwnurmum',
      #download_url='https://github.com/roglew/pappy-proxy/archive/%s.tar.gz'%VERSION,
      install_requires=[
          'cmd2>=0.6.8',
          'Jinja2>=2.8',
          'pygments>=2.0.2',
          ],
      classifiers=[
          'Intended Audience :: Developers',
          'Intended Audience :: Information Technology',
          'Operating System :: MacOS',
          'Operating System :: POSIX :: Linux',
          'Development Status :: 2 - Pre-Alpha',
          'Programming Language :: Python :: 3.6',
          'License :: OSI Approved :: MIT License',
          'Topic :: Security',
        ]
)

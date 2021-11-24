Format: 1.0
Source: lustre
Binary: lustre-source, lustre-client-utils, lustre-server-utils, lustre-resource-agents, lustre-iokit, lustre-tests, lustre-dev, lustre-client-modules-dkms
Architecture: all i386 armhf powerpc ppc64el amd64 ia64 arm64
Version: 2.12.4.1-cray-141-g0918c8f-1
Maintainer: Brian J. Murrell <brian.murrell@intel.com>
Uploaders: Brian J. Murrell <brian.murrell@intel.com>
Homepage: https://wiki.whamcloud.com/
Standards-Version: 3.8.3
Vcs-Git: git://git.whamcloud.com/fs/lustre-release.git
Build-Depends: module-assistant, libreadline-dev, debhelper (>= 9.0.0), dpatch, automake (>= 1.7) | automake1.7 | automake1.8 | automake1.9, pkg-config, libtool, libyaml-dev, libselinux-dev, libsnmp-dev, mpi-default-dev, bzip2, quilt, linux-headers-generic | linux-headers | linux-headers-amd64, rsync, libssl-dev
Package-List:
 lustre-client-modules-dkms deb admin optional arch=i386,armhf,powerpc,ppc64el,amd64,ia64
 lustre-client-utils deb utils optional arch=i386,armhf,powerpc,ppc64el,amd64,ia64,arm64
 lustre-dev deb libdevel optional arch=i386,armhf,powerpc,ppc64el,amd64,ia64,arm64
 lustre-iokit deb utils optional arch=i386,armhf,powerpc,ppc64el,amd64,ia64,arm64
 lustre-resource-agents deb ha optional arch=i386,armhf,powerpc,ppc64el,amd64,ia64,arm64
 lustre-server-utils deb utils optional arch=i386,armhf,powerpc,ppc64el,amd64,ia64,arm64
 lustre-source deb admin optional arch=all
 lustre-tests deb utils optional arch=i386,armhf,powerpc,ppc64el,amd64,ia64,arm64
Checksums-Sha1:
 b7566a2d2ffc6ded1c0f99d10eed640cad90d153 15711149 lustre_2.12.4.1-cray-141-g0918c8f-1.tar.gz
Checksums-Sha256:
 61171776d981073356b14512e1dca1d48c9cd11db0ee88504600a48c8d215d63 15711149 lustre_2.12.4.1-cray-141-g0918c8f-1.tar.gz
Files:
 927d8390f513ae27ae45ee2de3e983b0 15711149 lustre_2.12.4.1-cray-141-g0918c8f-1.tar.gz

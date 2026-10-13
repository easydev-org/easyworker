# Toolchain image: R.
#
# R is compiled from the CRAN source tarball. The distro's r-base would pull a
# large apt dependency tree and the Posit prebuilt binaries are access-gated,
# so a source build against the shared libraries the base already provides is
# the most reproducible option. CRAN itself is a plain HTTP tree (PACKAGES +
# tarballs), with no artifact protocol.
ARG BASE_IMAGE=toolchain-base:debian-trixie
FROM ${BASE_IMAGE}
# OS libraries R links against (not toolchains): BLAS/LAPACK, readline,
# compression, PNG/JPEG/TIFF, cairo, X11 headers for the default packages.
# The base image already ships the official Debian sources; the build runs
# through the buildkitd proxy, so deb.debian.org is used directly (same policy
# as base.Dockerfile) and nothing is restored afterwards.
RUN set -eux; \
    echo "apt via ${HTTP_PROXY:-direct}"; \
    for i in 1 2 3 4 5 6; do \
      apt-get -o Acquire::Retries=10 update && \
      apt-get -o Acquire::Retries=10 install -y --no-install-recommends \
        gfortran libreadline-dev libblas-dev liblapack-dev libpcre2-dev libcurl4-openssl-dev \
        libdeflate-dev libbz2-dev liblzma-dev zlib1g-dev libpng-dev libjpeg-dev \
        libtiff-dev libicu-dev libx11-dev libxt-dev libcairo2-dev texinfo \
      && break || { echo "apt retry $i"; sleep 3; }; \
    done; \
    rm -rf /var/lib/apt/lists/*
COPY cache/R-4.6.1.tar.gz /tmp/R.tar.gz
RUN set -eux; \
    tar -xzf /tmp/R.tar.gz -C /tmp && rm /tmp/R.tar.gz; \
    cd /tmp/R-4.6.1; \
    ./configure --prefix=/opt/R --enable-R-shlib --with-blas --with-lapack --with-x=no; \
    make -j"$(nproc)"; \
    make install; \
    cd /; rm -rf /tmp/R-4.6.1
ENV PATH=/opt/R/bin:$PATH \
    R_HOME=/opt/R/lib/R
RUN R --version | head -1

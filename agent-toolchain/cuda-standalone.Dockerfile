# syntax=docker/dockerfile:1
# Self-contained CUDA sandbox image (no toolchain image dependency).
#
# Built FROM the official Debian base (pulled through the artifact mirror) and
# installs the CUDA user-space stack + cuDNN + CPython 3.13 + uv, then bakes
# agent-worker. The host DRIVER (/dev/nvidia*, libcuda.so) is NOT here — a
# sandbox gets it at runtime from the NVIDIA device plugin + the `nvidia`
# RuntimeClass.
#
# The cpython/uv tarballs come from the build context (fetch-artifacts.sh).
FROM docker.io/library/debian:trixie-slim

ARG HTTP_PROXY
ARG HTTPS_PROXY
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=localhost,127.0.0.1,.svc.cluster.local,.svc,.nip.io,10.199.64.20,artifact.worker.svc.cluster.local \
    no_proxy=localhost,127.0.0.1,.svc.cluster.local,.svc,.nip.io,10.199.64.20,artifact.worker.svc.cluster.local \
    DEBIAN_FRONTEND=noninteractive

ARG CUDA_MAJOR=13
ARG CUDA_MINOR=4

# --- CUDA toolkit + cuDNN from NVIDIA's Debian 13 repo ----------------------
RUN set -eux; \
    apt-get -o Acquire::Retries=10 update; \
    apt-get -o Acquire::Retries=10 install -y --no-install-recommends \
      ca-certificates curl gnupg cmake ninja-build; \
    curl -fsSL --retry 5 \
      "https://developer.download.nvidia.com/compute/cuda/repos/debian13/x86_64/cuda-keyring_1.1-1_all.deb" \
      -o /tmp/cuda-keyring.deb; \
    dpkg -i /tmp/cuda-keyring.deb; rm -f /tmp/cuda-keyring.deb; \
    apt-get -o Acquire::Retries=10 update; \
    for i in 1 2 3 4 5 6; do \
      apt-get -o Acquire::Retries=10 install -y --no-install-recommends \
        "cuda-compiler-${CUDA_MAJOR}-${CUDA_MINOR}" \
        "cuda-libraries-${CUDA_MAJOR}-${CUDA_MINOR}" \
        "cuda-libraries-dev-${CUDA_MAJOR}-${CUDA_MINOR}" \
        "cuda-nvml-dev-${CUDA_MAJOR}-${CUDA_MINOR}" \
        "cuda-command-line-tools-${CUDA_MAJOR}-${CUDA_MINOR}" \
        libcudnn9-dev-cuda-13 \
      && break || { echo "apt retry $i"; sleep 3; }; \
    done; \
    ln -sfn "/usr/local/cuda-${CUDA_MAJOR}.${CUDA_MINOR}" /usr/local/cuda; \
    rm -rf /var/lib/apt/lists/*
ENV CUDA_HOME=/usr/local/cuda \
    PATH=/usr/local/cuda/bin:$PATH \
    LD_LIBRARY_PATH=/usr/local/cuda/lib64 \
    LIBRARY_PATH=/usr/local/cuda/lib64/stubs

# Stub driver libs as the LOWEST-priority search path (sort AFTER zz-nvcr-*).
RUN set -eux; \
    printf '/usr/local/cuda/lib64/stubs\n' > /etc/ld.so.conf.d/zzz-cuda-stubs.conf; \
    ldconfig

# --- CPython 3.13 + uv (from the build context) -----------------------------
COPY cache/cpython-3.13.15+20260924-x86_64-unknown-linux-gnu-install_only.tar.gz /tmp/py313.tar.gz
COPY cache/uv-x86_64-unknown-linux-gnu.tar.gz /tmp/uv.tar.gz
RUN set -eux; \
    tar -xzf /tmp/py313.tar.gz -C /opt && rm /tmp/py313.tar.gz; \
    mv /opt/python /opt/python-3.13.15; \
    ln -s /opt/python-3.13.15 /opt/python; \
    mkdir -p /opt/uv; \
    tar -xzf /tmp/uv.tar.gz -C /opt/uv --strip-components=1 && rm /tmp/uv.tar.gz
ENV PATH=/opt/python/bin:/opt/uv:$PATH \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# --- agent-worker (baked in; the ONLY thing a sandbox may run) --------------
COPY agent-worker /usr/local/bin/agent-worker
RUN mkdir -p /data
ENV WORKER_PORT=48080 \
    WORKER_DB=/data/jobs.db
EXPOSE 48080
ENTRYPOINT ["/usr/local/bin/agent-worker"]

#!/bin/bash
set -ex

PROJECT_PATH=$(cd "$(dirname "$0")/.."; pwd)
TARGET_PATH="${PROJECT_PATH}/src/k8s.io/autoscaler"
SHELL_PATH="${TARGET_PATH}/cluster-autoscaler/cloudprovider/hws/deploy"
args="$#"

if [[ "${args}" != "4" ]]; then
    echo "##### wrong args numbers, please check~~~"
    exit 1
fi

local_version=${local_version}
build_env=${1:-"image mr"} # mr为门禁触发
isLayer=${2:-"nolayers"}
BASE_IMAGE_amd64=${3:-"registry-cbu.huawei.com/paas-cce/euleros_x86_64:x.x.x"}
BASE_IMAGE_arm64=${4:-"registry-cbu.huawei.com/paas-cce/euleros_arm:x.x.x"}
platform=$(uname -m)

function install_dependent_go_version() {
    cur_go_version=`go version | awk '{print $3}' | cut -b 3-`
    except_go_version="1.20"
    if [[ "${cur_go_version}" > "${except_go_version}" ]]; then
       return
    fi
    # 从北冥仓下载golang文件
    if [[ "${platform}" == "x86_64" ]]; then
        bm -action download -name cce-golang -version 1.20.5-amd64 -release latest -output ${WORKSPACE}
        tar -C /usr/local -xzf ${WORKSPACE}/go1.20.5.linux-amd64.tar.gz
        export GOTOOLDIR="/usr/local/go/pkg/tool/linux_amd64"
    elif [[ "${platform}" == "aarch64" ]]; then
        bm -action download -name cce-golang -version 1.20.5-arm64 -release latest -output ${WORKSPACE}
        tar -C /usr/local -xzf ${WORKSPACE}/go1.20.5.linux-arm64.tar.gz
        export GOTOOLDIR="/usr/local/go/pkg/tool/linux_arm64"
    else
        echo "args ARCH_TYPE=${ARCH_TYPE} value is wrong!!!"
    fi
    echo "export PATH=/usr/local/go/bin:${PATH}" | sudo tee -a $HOME/.profile
    source $HOME/.profile
    export GOROOT="/usr/local/go"
}

function prepare_env() {
    if [[ -n "${CID_WORKSPACE}" ]]; then
        echo "##### 存在伏羲工作目录环境变量,使用该变量作为WORKSPACE"
        WORKSPACE="${CID_WORKSPACE}"
    else
        echo "##### 自定义WORKSPACE"
        WORKSPACE=${PROJECT_PATH}/..
    fi

    if [[ "${platform}" == "aarch64" ]]; then
        BASE_IMAGE="${BASE_IMAGE_arm64}"
        PLATFORM="linux-arm64"
    elif [[ "${platform}" == "x86_64" ]]; then
        BASE_IMAGE="${BASE_IMAGE_amd64}"
        PLATFORM="linux-amd64"
    else
        echo "Not support platform: ${platform}"
        exit 1
    fi
BASE_IMAGE_NAME=`echo ${BASE_IMAGE} | awk -F ":" '{print $1}'`
BASE_IMAGE_TAG=`echo ${BASE_IMAGE} | awk -F ":" '{print $2}'`
}

function prepare_code() {
    pushd ${PROJECT_PATH}
      TARGET_PATH="${PROJECT_PATH}/src/k8s.io/autoscaler"
      if [[ -d ${TARGET_PATH} ]]; then
          rm -rf ${TARGET_PATH}
      fi
      mkdir -p ${TARGET_PATH}
      cp -rf `ls -a ${PROJECT_PATH} | grep -E -v "^(src)$" | grep -v "^.$" | grep -v "^..$"` ${TARGET_PATH}
    popd
}

function get_version_from_chart() {
    local ADDON_NAME="autoscaler-1.27"
    local ADDONS_TEMPLATES_NAME="addons-templates"
    local CHART_PATH=`find ${WORKSPACE} -name ${ADDONS_TEMPLATES_NAME} -type d`
    local PACKAGE_FILE_PATH="${CHART_PATH}/addons/build/addons-package/${ADDON_NAME}-package.sh"
    export version=$(python ${CHART_PATH}/addons/build/addons-package/parseVersion.py "${CHART_PATH}/template/version.json" ${ADDON_NAME})

    if [[ -z "${version}" ]] || [[ ! -d "${CHART_PATH}" ]] || [[ ! -f "${PACKAGE_FILE_PATH}" ]]; then
        echo "get_version_from_chart failed!!! 请将 ${ADDONS_TEMPLATES_NAME} 下载至 ${WORKSPACE} 下"
        exit 2
    else
        echo "get_version_from_chart success."
    fi
}

function prepare_go_mod(){
    export GOSUMDB=off
    export GO111MODULE=on
    export GOPROXY=https://cmc.centralrepo.rnd.huawei.com/cbu-go
}

function build_autoscaler_binary() {
    # function only use for mr
    echo "begin to build cluster-autoscaler ..."
    pushd "$TARGET_PATH/cluster-autoscaler"
      gobuildmode="pie"
      go build -o cluster-autoscaler -ldflags " -linkmode=external -extldflags '-Wl,-z,now' " -buildmode ${gobuildmode} -trimpath
      strip cluster-autoscaler
      if [[ -f ./cluster-autoscaler ]]; then
          echo "cluster-autoscaler build successfully."
      else
          echo "cluster-autoscaler build failed."
          exit 1
      fi
    popd
}

function build_image() {
    pushd ${SHELL_PATH}
      bash -ex build_image.sh ${version} ${isLayer} ${PLATFORM} ${BASE_IMAGE_NAME} ${BASE_IMAGE_TAG}
      exit $?
    popd
}

prepare_env
install_dependent_go_version
prepare_code
prepare_go_mod
echo "##### build env is $build_env"
case $build_env in
  image)
    if [[ -n "${local_version}" ]]; then
        export version=${local_version}
    else
        get_version_from_chart
    fi
    build_image
    ;;
  mr)
    build_autoscaler_binary
    ;;
  *)
    echo "##### build env only support \"image/mr\"; not support $build_env"
    ;;
esac
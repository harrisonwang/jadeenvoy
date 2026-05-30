package tool

import "encoding/base64"

// base64Std 是 stdlib base64.StdEncoding 的别名，提取出来方便 file_tools 复用。
var base64Std = base64.StdEncoding

package browser

import "github.com/go-rod/rod/lib/proto"

func rodPageOpts(url string) proto.TargetCreateTarget {
	return proto.TargetCreateTarget{URL: url}
}

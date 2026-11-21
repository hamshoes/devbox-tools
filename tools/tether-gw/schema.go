package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

type sideSpec struct {
	App   string   `json:"app"`
	Calls []string `json:"calls"`
}

// manifest is the packaged manifest.json of a pair.
type manifest struct {
	Pair    string `json:"pair"`
	Version string `json:"version"`
	Schema  struct {
		SHA256 string `json:"sha256"`
	} `json:"schema"`
	VM     sideSpec `json:"vm"`
	Mac    sideSpec `json:"mac"`
	Limits struct {
		MaxMessage int64 `json:"max_message"`
	} `json:"limits"`
}

// method is one resolved rpc.
type method struct {
	in, out protoreflect.MessageDescriptor
}

// schema is a pair's manifest plus every method it lists, resolved.
type schema struct {
	man     manifest
	sha     string
	types   *dynamicpb.Types
	methods map[string]method   // "pkg.Service/Method"
	calls   map[string]string   // "pkg.Service/Method" → side that may call it
	sides   map[string]sideSpec // "vm" / "mac"
}

func loadSchema(dir string) (*schema, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	s := &schema{}
	if err := json.Unmarshal(raw, &s.man); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if s.man.Pair == "" || s.man.Version == "" {
		return nil, fmt.Errorf("manifest.json: pair and version are required")
	}
	if s.man.Limits.MaxMessage <= 0 {
		s.man.Limits.MaxMessage = 1 << 20
	}
	bin, err := os.ReadFile(filepath.Join(dir, "api.binpb"))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(bin)
	s.sha = hex.EncodeToString(sum[:])
	if s.sha != s.man.Schema.SHA256 {
		return nil, fmt.Errorf("api.binpb sha256 %s does not match manifest schema.sha256 %q", s.sha, s.man.Schema.SHA256)
	}
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(bin, &fds); err != nil {
		return nil, fmt.Errorf("api.binpb: %w", err)
	}
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		return nil, fmt.Errorf("api.binpb: %w", err)
	}
	s.types = dynamicpb.NewTypes(files)
	s.sides = map[string]sideSpec{"vm": s.man.VM, "mac": s.man.Mac}
	s.methods = map[string]method{}
	s.calls = map[string]string{}
	for side, spec := range s.sides {
		for _, name := range spec.Calls {
			m, err := resolve(files, name)
			if err != nil {
				return nil, fmt.Errorf("%s.calls: %w", side, err)
			}
			if prev, dup := s.calls[name]; dup {
				return nil, fmt.Errorf("%s is listed under both %s and %s", name, prev, side)
			}
			s.methods[name] = m
			s.calls[name] = side
		}
	}
	return s, nil
}

func resolve(files *protoregistry.Files, name string) (method, error) {
	svcName, rpc, ok := strings.Cut(name, "/")
	if !ok {
		return method{}, fmt.Errorf("%q is not <pkg.Service>/<Method>", name)
	}
	d, err := files.FindDescriptorByName(protoreflect.FullName(svcName))
	if err != nil {
		return method{}, fmt.Errorf("%s: unknown service", name)
	}
	svc, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return method{}, fmt.Errorf("%s: %s is not a service", name, svcName)
	}
	md := svc.Methods().ByName(protoreflect.Name(rpc))
	if md == nil {
		return method{}, fmt.Errorf("%s: unknown method", name)
	}
	if md.IsStreamingClient() || md.IsStreamingServer() {
		return method{}, fmt.Errorf("%s: streaming methods are not supported", name)
	}
	return method{in: md.Input(), out: md.Output()}, nil
}

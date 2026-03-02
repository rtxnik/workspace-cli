package vless

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		want    VLESSConfig
		wantErr bool
	}{
		{
			name: "tcp-reality",
			uri:  "vless://uuid-1234@example.com:443?encryption=none&flow=xtls-rprx-vision&type=tcp&security=reality&sni=www.google.com&fp=chrome&pbk=pub123&sid=ab&spx=%2F#my-node",
			want: VLESSConfig{
				UUID: "uuid-1234", Address: "example.com", Port: 443,
				Encryption: "none", Flow: "xtls-rprx-vision",
				Network: "tcp", Security: "reality",
				SNI: "www.google.com", Fp: "chrome",
				PublicKey: "pub123", ShortID: "ab", SpiderX: "/",
				Remark: "my-node",
			},
		},
		{
			name: "tcp-http-header",
			uri:  "vless://uuid-5678@example.com:80?type=tcp&security=none&headerType=http&host=cdn.example.com&path=%2F#http-node",
			want: VLESSConfig{
				UUID: "uuid-5678", Address: "example.com", Port: 80,
				Encryption: "none",
				Network: "tcp", Security: "none", Fp: "chrome",
				HeaderType: "http", Host: "cdn.example.com", Path: "/",
				Remark: "http-node",
			},
		},
		{
			name: "websocket-tls",
			uri:  "vless://uuid-ws@ws.example.com:443?type=ws&security=tls&sni=ws.example.com&fp=firefox&host=ws.example.com&path=%2Fvless-ws#ws-tls",
			want: VLESSConfig{
				UUID: "uuid-ws", Address: "ws.example.com", Port: 443,
				Encryption: "none",
				Network: "ws", Security: "tls",
				SNI: "ws.example.com", Fp: "firefox",
				Host: "ws.example.com", Path: "/vless-ws",
				Remark: "ws-tls",
			},
		},
		{
			name: "grpc-reality",
			uri:  "vless://uuid-grpc@grpc.example.com:443?type=grpc&security=reality&sni=www.google.com&fp=chrome&pbk=grpc-pub&sid=cd&serviceName=mygrpc#grpc-node",
			want: VLESSConfig{
				UUID: "uuid-grpc", Address: "grpc.example.com", Port: 443,
				Encryption: "none",
				Network: "grpc", Security: "reality",
				SNI: "www.google.com", Fp: "chrome",
				PublicKey: "grpc-pub", ShortID: "cd",
				ServiceName: "mygrpc",
				Remark: "grpc-node",
			},
		},
		{
			name: "h2-tls",
			uri:  "vless://uuid-h2@h2.example.com:443?type=h2&security=tls&sni=h2.example.com&fp=chrome&host=h2.example.com&path=%2Fh2path#h2-node",
			want: VLESSConfig{
				UUID: "uuid-h2", Address: "h2.example.com", Port: 443,
				Encryption: "none",
				Network: "h2", Security: "tls",
				SNI: "h2.example.com", Fp: "chrome",
				Host: "h2.example.com", Path: "/h2path",
				Remark: "h2-node",
			},
		},
		{
			name: "httpupgrade-tls",
			uri:  "vless://uuid-hu@hu.example.com:443?type=httpupgrade&security=tls&sni=hu.example.com&fp=safari&host=hu.example.com&path=%2Fupgrade#hu-node",
			want: VLESSConfig{
				UUID: "uuid-hu", Address: "hu.example.com", Port: 443,
				Encryption: "none",
				Network: "httpupgrade", Security: "tls",
				SNI: "hu.example.com", Fp: "safari",
				Host: "hu.example.com", Path: "/upgrade",
				Remark: "hu-node",
			},
		},
		{
			name: "xhttp-reality",
			uri:  "vless://uuid-xhttp@xhttp.example.com:443?type=xhttp&security=reality&sni=www.google.com&fp=chrome&pbk=xhttp-pub&sid=ef&path=%2Fxpath&mode=auto#xhttp-node",
			want: VLESSConfig{
				UUID: "uuid-xhttp", Address: "xhttp.example.com", Port: 443,
				Encryption: "none",
				Network: "xhttp", Security: "reality",
				SNI: "www.google.com", Fp: "chrome",
				PublicKey: "xhttp-pub", ShortID: "ef",
				Path: "/xpath", Mode: "auto",
				Remark: "xhttp-node",
			},
		},
		{
			name: "defaults applied",
			uri:  "vless://uuid-min@min.example.com:443#minimal",
			want: VLESSConfig{
				UUID: "uuid-min", Address: "min.example.com", Port: 443,
				Encryption: "none",
				Network: "tcp", Security: "none", Fp: "chrome",
				Remark: "minimal",
			},
		},
		{
			name:    "invalid scheme",
			uri:     "vmess://invalid",
			wantErr: true,
		},
		{
			name:    "missing UUID",
			uri:     "vless://example.com:443",
			wantErr: true,
		},
		{
			name:    "invalid port",
			uri:     "vless://uuid@example.com:abc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.uri)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			assertField(t, "UUID", got.UUID, tt.want.UUID)
			assertField(t, "Address", got.Address, tt.want.Address)
			assertIntField(t, "Port", got.Port, tt.want.Port)
			assertField(t, "Encryption", got.Encryption, tt.want.Encryption)
			assertField(t, "Flow", got.Flow, tt.want.Flow)
			assertField(t, "Network", got.Network, tt.want.Network)
			assertField(t, "Security", got.Security, tt.want.Security)
			assertField(t, "SNI", got.SNI, tt.want.SNI)
			assertField(t, "Fp", got.Fp, tt.want.Fp)
			assertField(t, "PublicKey", got.PublicKey, tt.want.PublicKey)
			assertField(t, "ShortID", got.ShortID, tt.want.ShortID)
			assertField(t, "SpiderX", got.SpiderX, tt.want.SpiderX)
			assertField(t, "Path", got.Path, tt.want.Path)
			assertField(t, "Host", got.Host, tt.want.Host)
			assertField(t, "ServiceName", got.ServiceName, tt.want.ServiceName)
			assertField(t, "Mode", got.Mode, tt.want.Mode)
			assertField(t, "HeaderType", got.HeaderType, tt.want.HeaderType)
			assertField(t, "Remark", got.Remark, tt.want.Remark)
		})
	}
}

func assertField(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func assertIntField(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", name, got, want)
	}
}

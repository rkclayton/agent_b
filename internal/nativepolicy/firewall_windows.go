//go:build windows

package nativepolicy

import (
	"encoding/hex"
	"fmt"
	"net"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type dispatch struct{ vtable *[7]uintptr }
type variant struct {
	VT, r1, r2, r3 uint16
	value          [8]byte
}
type dispatchParams struct {
	Args              *variant
	Named             *int32
	Count, NamedCount uint32
}
type firewallRule struct {
	Name, Description, RemoteAddresses, LocalUsers, ICMP string
	Direction, Action, Profiles, Protocol                int32
	Enabled                                              bool
}
type FirewallRequest struct {
	SID                         string
	AllowLocalNetwork           bool
	LocalSubnets, AllowedRanges []string
}

const firewallBlockName = "AgentB-Svc-Outbound-Block"
const firewallLegacyName = "AgentB-Svc-Model-Allow"
const firewallICMPName = "AgentB-Svc-LAN-ICMP-Allow"

var (
	ole32fw                = windows.NewLazySystemDLL("ole32.dll")
	oleaut32fw             = windows.NewLazySystemDLL("oleaut32.dll")
	procCoInitializeExFW   = ole32fw.NewProc("CoInitializeEx")
	procCoCreateInstanceFW = ole32fw.NewProc("CoCreateInstance")
	procCLSIDFromProgIDFW  = ole32fw.NewProc("CLSIDFromProgID")
	procCoUninitializeFW   = ole32fw.NewProc("CoUninitialize")
	procSysAllocStringFW   = oleaut32fw.NewProc("SysAllocString")
	procSysStringLenFW     = oleaut32fw.NewProc("SysStringLen")
	procVariantClearFW     = oleaut32fw.NewProc("VariantClear")
	iidDispatchFW          = windows.GUID{Data1: 0x00020400, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
)

func createDispatch(progID string) (*dispatch, func(), error) {
	runtime.LockOSThread()
	hr, _, _ := procCoInitializeExFW.Call(0, 2)
	initialized := int32(hr) >= 0
	cleanup := func() {
		if initialized {
			procCoUninitializeFW.Call()
		}
		runtime.UnlockOSThread()
	}
	if int32(hr) < 0 && uint32(hr) != 0x80010106 {
		cleanup()
		return nil, func() {}, fmt.Errorf("CoInitializeEx HRESULT 0x%x", hr)
	}
	name, _ := windows.UTF16PtrFromString(progID)
	var class windows.GUID
	if hr, _, _ = procCLSIDFromProgIDFW.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&class))); int32(hr) < 0 {
		cleanup()
		return nil, func() {}, fmt.Errorf("CLSIDFromProgID HRESULT 0x%x", hr)
	}
	var object *dispatch
	if hr, _, _ = procCoCreateInstanceFW.Call(uintptr(unsafe.Pointer(&class)), 0, 1, uintptr(unsafe.Pointer(&iidDispatchFW)), uintptr(unsafe.Pointer(&object))); int32(hr) < 0 {
		cleanup()
		return nil, func() {}, fmt.Errorf("CoCreateInstance HRESULT 0x%x", hr)
	}
	return object, func() { object.release(); cleanup() }, nil
}

func (d *dispatch) release() { syscall.SyscallN(d.vtable[2], uintptr(unsafe.Pointer(d))) }

func (d *dispatch) id(name string) (int32, error) {
	wide, _ := windows.UTF16PtrFromString(name)
	names := []*uint16{wide}
	var id int32
	hr, _, _ := syscall.SyscallN(d.vtable[5], uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(&windows.GUID{})), uintptr(unsafe.Pointer(&names[0])), 1, 0x400, uintptr(unsafe.Pointer(&id)))
	if int32(hr) < 0 {
		return 0, fmt.Errorf("COM name %s HRESULT 0x%x", name, hr)
	}
	return id, nil
}

func (d *dispatch) invoke(name string, flags uint16, arguments []variant) (variant, uintptr, error) {
	id, err := d.id(name)
	if err != nil {
		return variant{}, 0, err
	}
	params := dispatchParams{Count: uint32(len(arguments))}
	if len(arguments) > 0 {
		params.Args = &arguments[0]
	}
	propertyPut := int32(-3)
	if flags == 4 {
		params.Named, params.NamedCount = &propertyPut, 1
	}
	var result variant
	hr, _, _ := syscall.SyscallN(d.vtable[6], uintptr(unsafe.Pointer(d)), uintptr(id), uintptr(unsafe.Pointer(&windows.GUID{})), 0x400, uintptr(flags), uintptr(unsafe.Pointer(&params)), uintptr(unsafe.Pointer(&result)), 0, 0)
	if int32(hr) < 0 {
		return variant{}, hr, fmt.Errorf("COM %s HRESULT 0x%x", name, hr)
	}
	return result, hr, nil
}

func bstrVariant(value string) variant {
	wide, _ := windows.UTF16PtrFromString(value)
	ptr, _, _ := procSysAllocStringFW.Call(uintptr(unsafe.Pointer(wide)))
	result := variant{VT: 8}
	*(*uintptr)(unsafe.Pointer(&result.value[0])) = ptr
	return result
}
func i4Variant(value int32) variant {
	result := variant{VT: 3}
	*(*int32)(unsafe.Pointer(&result.value[0])) = value
	return result
}
func boolVariant(value bool) variant {
	result := variant{VT: 11}
	if value {
		*(*int16)(unsafe.Pointer(&result.value[0])) = -1
	}
	return result
}
func clearVariant(value *variant) { procVariantClearFW.Call(uintptr(unsafe.Pointer(value))) }

func (value *variant) pointer() unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&value.value[0]))
}

func (value *variant) int32() int32 {
	return *(*int32)(unsafe.Pointer(&value.value[0]))
}

func (d *dispatch) getDispatch(name string, args ...variant) (*dispatch, uintptr, error) {
	flags := uint16(2)
	if len(args) > 0 {
		flags = 1
	}
	value, hr, err := d.invoke(name, flags, args)
	if err != nil {
		return nil, hr, err
	}
	if value.VT != 9 || value.pointer() == nil {
		clearVariant(&value)
		return nil, hr, fmt.Errorf("COM %s did not return IDispatch", name)
	}
	return (*dispatch)(value.pointer()), hr, nil
}

func (d *dispatch) stringProperty(name string) (string, error) {
	value, _, err := d.invoke(name, 2, nil)
	if err != nil {
		return "", err
	}
	defer clearVariant(&value)
	if value.VT != 8 || value.pointer() == nil {
		return "", nil
	}
	length, _, _ := procSysStringLenFW.Call(uintptr(value.pointer()))
	return windows.UTF16ToString(unsafe.Slice((*uint16)(value.pointer()), int(length))), nil
}
func (d *dispatch) intProperty(name string) (int32, error) {
	value, _, err := d.invoke(name, 2, nil)
	if err != nil {
		return 0, err
	}
	defer clearVariant(&value)
	return value.int32(), nil
}
func (d *dispatch) boolProperty(name string) (bool, error) {
	value, _, err := d.invoke(name, 2, nil)
	if err != nil {
		return false, err
	}
	defer clearVariant(&value)
	return value.int32() != 0, nil
}

func (d *dispatch) set(name string, value variant) error {
	defer clearVariant(&value)
	_, _, err := d.invoke(name, 4, []variant{value})
	return err
}

func readFirewallRule(name string) (firewallRule, bool, error) {
	policy, done, err := createDispatch("HNetCfg.FwPolicy2")
	if err != nil {
		return firewallRule{}, false, err
	}
	defer done()
	rules, _, err := policy.getDispatch("Rules")
	if err != nil {
		return firewallRule{}, false, err
	}
	defer rules.release()
	argument := bstrVariant(name)
	defer clearVariant(&argument)
	rule, hr, err := rules.getDispatch("Item", argument)
	if err != nil {
		if uint32(hr) == 0x80070002 || uint32(hr) == 0x80070490 || uint32(hr) == 0x80020009 {
			return firewallRule{}, false, nil
		}
		return firewallRule{}, false, err
	}
	defer rule.release()
	result := firewallRule{}
	if result.Name, err = rule.stringProperty("Name"); err != nil {
		return firewallRule{}, false, err
	}
	if result.Description, err = rule.stringProperty("Description"); err != nil {
		return firewallRule{}, false, err
	}
	if result.RemoteAddresses, err = rule.stringProperty("RemoteAddresses"); err != nil {
		return firewallRule{}, false, err
	}
	if result.LocalUsers, err = rule.stringProperty("LocalUserAuthorizedList"); err != nil {
		return firewallRule{}, false, err
	}
	if result.ICMP, err = rule.stringProperty("IcmpTypesAndCodes"); err != nil {
		return firewallRule{}, false, err
	}
	if result.Direction, err = rule.intProperty("Direction"); err != nil {
		return firewallRule{}, false, err
	}
	if result.Action, err = rule.intProperty("Action"); err != nil {
		return firewallRule{}, false, err
	}
	if result.Profiles, err = rule.intProperty("Profiles"); err != nil {
		return firewallRule{}, false, err
	}
	if result.Protocol, err = rule.intProperty("Protocol"); err != nil {
		return firewallRule{}, false, err
	}
	if result.Enabled, err = rule.boolProperty("Enabled"); err != nil {
		return firewallRule{}, false, err
	}
	return result, true, nil
}

func removeFirewallRule(name string) error {
	policy, done, err := createDispatch("HNetCfg.FwPolicy2")
	if err != nil {
		return err
	}
	defer done()
	rules, _, err := policy.getDispatch("Rules")
	if err != nil {
		return err
	}
	defer rules.release()
	argument := bstrVariant(name)
	defer clearVariant(&argument)
	_, _, err = rules.invoke("Remove", 1, []variant{argument})
	if err != nil && strings.Contains(err.Error(), "80020009") {
		return nil
	}
	return err
}

func writeFirewallRule(spec firewallRule) error {
	policy, done, err := createDispatch("HNetCfg.FwPolicy2")
	if err != nil {
		return err
	}
	defer done()
	rules, _, err := policy.getDispatch("Rules")
	if err != nil {
		return err
	}
	defer rules.release()
	rule, release, err := createDispatch("HNetCfg.FWRule")
	if err != nil {
		return err
	}
	defer release()
	for name, value := range map[string]string{"Name": spec.Name, "Description": spec.Description, "RemoteAddresses": spec.RemoteAddresses, "LocalUserAuthorizedList": spec.LocalUsers} {
		if err := rule.set(name, bstrVariant(value)); err != nil {
			return err
		}
	}
	for name, value := range map[string]int32{"Direction": spec.Direction, "Action": spec.Action, "Profiles": spec.Profiles, "Protocol": spec.Protocol} {
		if err := rule.set(name, i4Variant(value)); err != nil {
			return err
		}
	}
	if spec.ICMP != "" {
		if err := rule.set("IcmpTypesAndCodes", bstrVariant(spec.ICMP)); err != nil {
			return err
		}
	}
	if err := rule.set("Enabled", boolVariant(spec.Enabled)); err != nil {
		return err
	}
	ruleVariant := variant{VT: 9}
	*(*unsafe.Pointer)(unsafe.Pointer(&ruleVariant.value[0])) = unsafe.Pointer(rule)
	_, _, err = rules.invoke("Add", 1, []variant{ruleVariant})
	runtime.KeepAlive(rule)
	return err
}

func InspectFirewallPolicy(request FirewallRequest) ([]Drift, error) {
	block, icmp, err := firewallIntent(request)
	if err != nil {
		return nil, err
	}
	var drift []Drift
	actual, found, err := readFirewallRule(block.Name)
	if err != nil {
		return nil, err
	}
	if !found {
		drift = append(drift, Drift{block.Name, "present", "missing"})
	} else {
		drift = append(drift, compareFirewallRule(block, actual, "")...)
	}
	if _, found, err := readFirewallRule(firewallLegacyName); err != nil {
		return nil, err
	} else if found {
		drift = append(drift, Drift{firewallLegacyName, "absent", "present"})
	}
	actualICMP, found, err := readFirewallRule(firewallICMPName)
	if err != nil {
		return nil, err
	}
	if request.AllowLocalNetwork {
		if !found {
			drift = append(drift, Drift{firewallICMPName, "present", "missing"})
		} else {
			drift = append(drift, compareFirewallRule(icmp, actualICMP, "ICMP.")...)
		}
	} else if found {
		drift = append(drift, Drift{firewallICMPName, "absent", "present"})
	}
	return drift, nil
}

func ApplyFirewallPolicy(request FirewallRequest, remove bool) error {
	for _, name := range []string{firewallBlockName, firewallLegacyName, firewallICMPName} {
		if err := removeFirewallRule(name); err != nil {
			return err
		}
	}
	if remove {
		return nil
	}
	block, icmp, err := firewallIntent(request)
	if err != nil {
		return err
	}
	if err := writeFirewallRule(block); err != nil {
		return err
	}
	if request.AllowLocalNetwork {
		if err := writeFirewallRule(icmp); err != nil {
			return err
		}
	}
	drift, err := InspectFirewallPolicy(request)
	if err != nil {
		return err
	}
	if len(drift) > 0 {
		return fmt.Errorf("firewall drift after write: %s expected %s got %s", drift[0].Subject, drift[0].Expected, drift[0].Found)
	}
	return nil
}

func firewallIntent(request FirewallRequest) (firewallRule, firewallRule, error) {
	metadata := net.ParseIP("169.254.169.254")
	for _, text := range request.AllowedRanges {
		ip, network, err := net.ParseCIDR(strings.TrimSpace(text))
		if err != nil || ip.To4() == nil {
			return firewallRule{}, firewallRule{}, fmt.Errorf("invalid IPv4 CIDR %q", text)
		}
		if network.Contains(metadata) {
			return firewallRule{}, firewallRule{}, fmt.Errorf("allowed range must not include the metadata endpoint: %q", text)
		}
	}
	for _, text := range request.LocalSubnets {
		ip, network, err := net.ParseCIDR(strings.TrimSpace(text))
		if err != nil || ip.To4() == nil {
			return firewallRule{}, firewallRule{}, fmt.Errorf("invalid local IPv4 CIDR %q", text)
		}
		ones, bits := network.Mask.Size()
		last := append(net.IP(nil), network.IP.To4()...)
		for bit := ones; bit < bits; bit++ {
			last[bit/8] |= 1 << uint(7-bit%8)
		}
		if !isPrivateIPv4(network.IP) || !isPrivateIPv4(last) {
			return firewallRule{}, firewallRule{}, fmt.Errorf("local subnet must be wholly RFC1918: %q", text)
		}
	}
	allowed := []string{"127.0.0.0/8"}
	allowed = append(allowed, request.AllowedRanges...)
	if request.AllowLocalNetwork {
		if len(request.LocalSubnets) == 0 {
			return firewallRule{}, firewallRule{}, fmt.Errorf("AllowLocalNetwork requires at least one confirmed subnet")
		}
		allowed = append(allowed, request.LocalSubnets...)
	}
	blocked, err := blockedIPv4Ranges(allowed)
	if err != nil {
		return firewallRule{}, firewallRule{}, err
	}
	blocked = append(blocked, "::", "::2-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")
	localUsers := "D:(A;;CC;;;" + request.SID + ")"
	description := "Agent_b allowed=" + strings.Join(request.AllowedRanges, ",") + " lan=" + strings.Join(request.LocalSubnets, ",")
	block := firewallRule{Name: firewallBlockName, Description: description, RemoteAddresses: strings.Join(blocked, ","), LocalUsers: localUsers, Direction: 2, Action: 0, Profiles: 0x7fffffff, Protocol: 256, Enabled: true}
	icmp := firewallRule{Name: firewallICMPName, Description: "Allows outbound ICMPv4 echo to user-confirmed LAN subnets for the Agent_b service identity.", RemoteAddresses: strings.Join(request.LocalSubnets, ","), LocalUsers: localUsers, ICMP: "8:*", Direction: 2, Action: 1, Profiles: 0x7fffffff, Protocol: 1, Enabled: true}
	return block, icmp, nil
}

func isPrivateIPv4(ip net.IP) bool {
	ip = ip.To4()
	return ip != nil && ip.IsPrivate()
}

func compareFirewallRule(want, got firewallRule, prefix string) []Drift {
	var drift []Drift
	compare := func(name string, expected, found any) {
		if fmt.Sprint(expected) != fmt.Sprint(found) {
			drift = append(drift, Drift{want.Name + " " + prefix + name, fmt.Sprint(expected), fmt.Sprint(found)})
		}
	}
	compare("Direction", want.Direction, got.Direction)
	compare("Action", want.Action, got.Action)
	compare("Enabled", want.Enabled, got.Enabled)
	compare("Profile", want.Profiles, got.Profiles)
	compare("Description", want.Description, got.Description)
	compare("LocalUser", want.LocalUsers, got.LocalUsers)
	compare("Protocol", want.Protocol, got.Protocol)
	if !addressSetsEqual(want.RemoteAddresses, got.RemoteAddresses) {
		drift = append(drift, Drift{want.Name + " " + prefix + "RemoteAddress", want.RemoteAddresses, got.RemoteAddresses})
	}
	if want.ICMP != got.ICMP {
		compare("IcmpType", want.ICMP, got.ICMP)
	}
	return drift
}

func blockedIPv4Ranges(allowed []string) ([]string, error) {
	type span struct{ first, last uint32 }
	ranges := []span{{0, ^uint32(0)}}
	for _, text := range allowed {
		ip, network, err := net.ParseCIDR(strings.TrimSpace(text))
		if err != nil || ip.To4() == nil {
			return nil, fmt.Errorf("invalid IPv4 CIDR %q", text)
		}
		ones, bits := network.Mask.Size()
		if bits != 32 || ones < 8 {
			return nil, fmt.Errorf("IPv4 prefix must be /8 through /32: %q", text)
		}
		startBytes := network.IP.To4()
		start := uint32(startBytes[0])<<24 | uint32(startBytes[1])<<16 | uint32(startBytes[2])<<8 | uint32(startBytes[3])
		end := start | ^uint32(0)>>ones
		var next []span
		for _, current := range ranges {
			if end < current.first || start > current.last {
				next = append(next, current)
				continue
			}
			if start > current.first {
				next = append(next, span{current.first, start - 1})
			}
			if end < current.last {
				next = append(next, span{end + 1, current.last})
			}
		}
		ranges = next
	}
	format := func(value uint32) string {
		return fmt.Sprintf("%d.%d.%d.%d", byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
	}
	result := make([]string, 0, len(ranges))
	for _, item := range ranges {
		if item.first == item.last {
			result = append(result, format(item.first))
		} else {
			result = append(result, format(item.first)+"-"+format(item.last))
		}
	}
	return result, nil
}

func addressSetsEqual(first, second string) bool {
	a, err := addressIntervals(first)
	if err != nil {
		return false
	}
	b, err := addressIntervals(second)
	if err != nil || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func addressIntervals(value string) ([]string, error) {
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "-", 2)
		first := net.ParseIP(parts[0])
		if first == nil {
			_, network, err := net.ParseCIDR(item)
			if err != nil {
				return nil, err
			}
			first = network.IP
			last := make(net.IP, len(first))
			for i := range first {
				last[i] = first[i] | ^network.Mask[i]
			}
			result = append(result, intervalKey(first, last))
			continue
		}
		last := first
		if len(parts) == 2 {
			last = net.ParseIP(parts[1])
			if last == nil {
				return nil, fmt.Errorf("invalid address %s", parts[1])
			}
		}
		result = append(result, intervalKey(first, last))
	}
	sort.Strings(result)
	return result, nil
}
func intervalKey(first, last net.IP) string {
	if v4 := first.To4(); v4 != nil {
		first = v4
		last = last.To4()
	} else {
		first, last = first.To16(), last.To16()
	}
	return hex.EncodeToString(first) + "-" + hex.EncodeToString(last)
}

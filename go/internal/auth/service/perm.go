package service

const (
	PermPublic        = "public"
	PermAgentRead     = "agent.read"
	PermAgentWrite    = "agent.write"
	PermSessionWrite  = "session.write"
	PermApprovalWrite = "approval.write"
	PermScheduleWrite = "schedule.write"
	PermSecretWrite   = "secret.write"
	PermMemberWrite   = "member.write"
	PermTenantAdmin   = "tenant.admin"
)

var rank = map[string]int{
	"viewer":    1,
	"developer": 2,
	"admin":     3,
	"owner":     4,
}

var rolePerms = map[string]map[string]bool{}

func init() {
	read := map[string]bool{PermAgentRead: true}
	dev := clone(read)
	dev[PermSessionWrite] = true
	dev[PermAgentWrite] = true
	dev[PermApprovalWrite] = true
	dev[PermScheduleWrite] = true
	admin := clone(dev)
	admin[PermSecretWrite] = true
	admin[PermMemberWrite] = true
	owner := clone(admin)
	owner[PermTenantAdmin] = true
	rolePerms["viewer"] = read
	rolePerms["developer"] = dev
	rolePerms["admin"] = admin
	rolePerms["owner"] = owner
}

func clone(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func Allowed(role, perm string) bool {
	if perm == PermPublic {
		return true
	}
	return rolePerms[role][perm]
}

func RoleRank(role string) (int, bool) {
	n, ok := rank[role]
	return n, ok
}

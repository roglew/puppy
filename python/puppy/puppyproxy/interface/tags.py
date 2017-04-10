from ..util import confirm

def tag_cmd(client, args):
    if len(args) == 0:
        raise CommandError("Usage: tag <tag> [reqid1] [reqid2] ...")
    if not args[0]:
        raise CommandError("Tag cannot be empty")
    tag = args[0]
    reqids = []
    if len(args) > 1:
        for reqid in args[1:]:
            client.add_tag(reqid, tag)
    else:
        icr = client.in_context_requests(headers_only=True)
        cnt = confirm("You are about to tag {} requests with \"{}\". Continue?".format(len(icr), tag))
        if not cnt:
            return
        for reqh in icr:
            reqid = client.prefixed_reqid(reqh)
            client.remove_tag(reqid, tag)
            
def untag_cmd(client, args):
    if len(args) == 0:
        raise CommandError("Usage: untag <tag> [reqid1] [reqid2] ...")
    if not args[0]:
        raise CommandError("Tag cannot be empty")
    tag = args[0]
    reqids = []
    if len(args) > 0:
        for reqid in args[1:]:
            client.remove_tag(reqid, tag)
    else:
        icr = client.in_context_requests(headers_only=True)
        cnt = confirm("You are about to remove the \"{}\" tag from {} requests. Continue?".format(tag, len(icr)))
        if not cnt:
            return
        for reqh in icr:
            reqid = client.prefixed_reqid(reqh)
            client.add_tag(reqid, tag)

def clrtag_cmd(client, args):
    if len(args) == 0:
        raise CommandError("Usage: clrtag [reqid1] [reqid2] ...")
    reqids = []
    if len(args) > 0:
        for reqid in args:
            client.clear_tag(reqid)
    else:
        icr = client.in_context_requests(headers_only=True)
        cnt = confirm("You are about to clear ALL TAGS from {} requests. Continue?".format(len(icr)))
        if not cnt:
            return
        for reqh in icr:
            reqid = client.prefixed_reqid(reqh)
            client.clear_tag(reqid)

def load_cmds(cmd):
    cmd.set_cmds({
        'clrtag': (clrtag_cmd, None),
        'untag': (untag_cmd, None),
        'tag': (tag_cmd, None),
    })

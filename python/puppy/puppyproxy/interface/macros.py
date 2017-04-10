from ..macros import macro_from_requests, MacroTemplate, load_macros

macro_dict = {}

def generate_macro(client, args):
    if len(args) == 0:
        print("usage: gma [name] [reqids]")
        return
    macro_name = args[0]

    reqs = []
    if len(args) > 1:
        ids = args[1].split(',')
        for reqid in ids:
            req = client.req_by_id(reqid)
            reqs.append(req)

    script_string = macro_from_requests(reqs)
    fname = MacroTemplate.template_filename('macro', macro_name)
    with open(fname, 'w') as f:
        f.write(script_string)
    print("Macro written to {}".format(fname))
    
def load_macros_cmd(client, args):
    global macro_dict

    load_dir = '.'
    if len(args) > 0:
        load_dir = args[0]

    loaded_macros, loaded_int_macros = load_macros(load_dir)
    for macro in loaded_macros:
        macro_dict[macro.name] = macro
        print("Loaded {} ({})".format(macro.name, macro.file_name))

def complete_run_macro(text, line, begidx, endidx):
    from ..util import autocomplete_starts_with

    global macro_dict
    strs = [k for k,v in macro_dict.iteritems()]
    return autocomplete_startswith(text, strs)
        
def run_macro(client, args):
    global macro_dict
    if len(args) == 0:
        print("usage: rma [macro name]")
        return
    macro = macro_dict[args[0]]
    macro.execute(client, args[1:])

def load_cmds(cmd):
    cmd.set_cmds({
        'generate_macro': (generate_macro, None),
        'load_macros': (load_macros_cmd, None),
        'run_macro': (run_macro, complete_run_macro),
    })
    cmd.add_aliases([
        ('generate_macro', 'gma'),
        ('load_macros', 'lma'),
        ('run_macro', 'rma'),
    ])

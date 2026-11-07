// The groups the signed-in account sees: every group by name, its own role
// in each, and the groups it is invited to.

import type { Group, Invitation } from "../gen/docsearch/group/v1/group_pb";
import { groups as service, problem } from "./api";

class Groups {
  all = $state<Group[]>([]);
  invitations = $state<Invitation[]>([]);
  /** Whether this account may create a group on this docsearch. */
  canCreate = $state(false);
  error = $state("");
  loaded = $state(false);

  /** The groups this account is in. */
  get mine(): Group[] {
    return this.all.filter((g) => g.role !== "");
  }

  /** The groups this account maintains, which it may share a library with. */
  get maintained(): Group[] {
    return this.all.filter((g) => g.role === "maintainer");
  }

  byId(slug: string): Group | undefined {
    return this.all.find((g) => g.group === slug);
  }

  async load(): Promise<void> {
    try {
      const [list, { invitations }] = await Promise.all([service.list({}), service.invitations({})]);
      this.all = list.groups;
      this.canCreate = list.canCreate;
      this.invitations = invitations;
      this.error = "";
    } catch (err) {
      this.error = problem(err);
    } finally {
      this.loaded = true;
    }
  }

  async create(slug: string, name: string): Promise<void> {
    await service.create({ group: slug, name });
    await this.load();
  }

  async accept(slug: string): Promise<void> {
    await service.accept({ group: slug });
    await this.load();
  }

  async decline(slug: string): Promise<void> {
    await service.decline({ group: slug });
    this.invitations = this.invitations.filter((i) => i.group !== slug);
  }
}

export const groupsStore = new Groups();
